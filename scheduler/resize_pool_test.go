package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test helpers ---

var silentLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// terminableNode tracks whether Terminate was called.
type terminableNode struct {
	name       string
	terminated chan struct{}
}

func newTerminableNode(name string) *terminableNode {
	return &terminableNode{name: name, terminated: make(chan struct{})}
}

func (n *terminableNode) Name() string { return n.name }
func (n *terminableNode) RunTask(_ context.Context, _ *Task, _ RunTaskConfig) (int, error) {
	return 0, nil
}
func (n *terminableNode) Terminate() error {
	close(n.terminated)
	return nil
}

// makeJob creates a Job with the given properties.
func makeJob(name string, taskNames ...string) *Job {
	return &Job{
		Job: &proto.Job{
			Name: name,
			Tasks: lo.Map(taskNames, func(n string, _ int) *proto.Job_Task {
				return &proto.Job_Task{Name: n}
			}),
		},
		id: "test",
	}
}

// makeJobWithSlots creates a Job with tasks having specific slot counts.
func makeJobWithSlots(name string, tasks map[string]uint32) *Job {
	var jobTasks []*proto.Job_Task
	for taskName, slots := range tasks {
		jobTasks = append(jobTasks, &proto.Job_Task{Name: taskName, Slots: slots})
	}
	return &Job{
		Job: &proto.Job{Name: name, Tasks: jobTasks},
		id:  "test",
	}
}

// makeTasksForJob creates Task objects for every task in the job.
func makeTasksForJob(job *Job) []*Task {
	var tasks []*Task
	for _, jobTask := range job.Tasks {
		slots := int(jobTask.Slots)
		if slots < 1 {
			slots = 1
		}
		tasks = append(tasks, &Task{
			Job:   job,
			Name:  jobTask.Name,
			Slots: slots,
			Log:   silentLogger,
		})
	}
	return tasks
}

// makeOnlineNode creates an online nodeState with the given slot count and a terminableNode.
func makeOnlineNode(s *Scheduler, name string) (*nodeState, *terminableNode) {
	node := newTerminableNode(name)
	ns := &nodeState{
		scheduler: s,
		node:      node,
		status:    NodeStatusOnline,
		tasks:     make([]*Task, s.config.SlotsPerNode),
		log:       silentLogger,
		nodeName:  name,
	}
	return ns, node
}

// makeQueuedNode creates a queued nodeState in the nodesQueue.
func makeQueuedNode(s *Scheduler, name string, earliestStart time.Time) *nodeState {
	return &nodeState{
		scheduler:     s,
		status:        NodeStatusQueued,
		tasks:         make([]*Task, s.config.SlotsPerNode),
		log:           silentLogger,
		nodeName:      name,
		earliestStart: earliestStart,
	}
}

// --- Tests ---

func TestResizePool_IdleNodeTerminated_WhenNoQueuedTasks(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 10
	config.SlotsPerNode = 1
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Schedule a job — will create a node
	job := newTestJob("task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	// Wait for it to complete (node becomes idle)
	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	// Wait for the idle node to be terminated
	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventNodeTerminated)
		return ok
	})

	hasNodeTerminated := false
	for _, e := range events {
		if _, ok := e.(EventNodeTerminated); ok {
			hasNodeTerminated = true
		}
	}
	assert.True(t, hasNodeTerminated, "idle node should be terminated when no tasks are queued")
}

func TestResizePool_IdleNodeKept_WhenTasksQueued(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 10
	s := New(prov, config)

	// Create an idle online node
	ns, node := makeOnlineNode(s, "node-a")
	s.nodes = append(s.nodes, ns)

	// Queue tasks
	job := makeJob("job-a", "task-1")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// The idle node should NOT be terminated (queued tasks exist)
	select {
	case <-node.terminated:
		t.Fatal("idle node with queued tasks should NOT have been terminated")
	case <-time.After(200 * time.Millisecond):
		// Good: node was kept
	}
}

func TestResizePool_CreatesNodes(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 10
	config.SlotsPerNode = 1
	config.ProvisioningDelay = 0
	s := New(prov, config)

	// Queue 4 tasks
	job := makeJob("job-a", "a1", "a2", "a3", "a4")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	allNodes := append(s.nodes, s.nodesQueue...)
	assert.GreaterOrEqual(t, len(allNodes), 1, "expected at least 1 node to be created")
}

func TestResizePool_RespectsMaxNodes(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 3
	config.SlotsPerNode = 1
	config.ProvisioningDelay = 0
	s := New(prov, config)

	// Queue 10 tasks (would need 10 nodes at slotsPerNode=1)
	job := makeJob("job-a", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	totalNodes := len(s.nodes) + len(s.nodesQueue)
	assert.LessOrEqual(t, totalNodes, config.MaxNodes,
		"total nodes (%d) should not exceed MaxNodes (%d)", totalNodes, config.MaxNodes)
}

func TestResizePool_DiscardedNodesRemoved(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	s := New(prov, config)

	// Create a discarded node
	ns := &nodeState{
		scheduler: s,
		status:    NodeStatusDiscarded,
		tasks:     make([]*Task, 1),
		log:       silentLogger,
		nodeName:  "dead-node",
	}
	s.nodes = append(s.nodes, ns)

	s.resizePool()

	assert.Len(t, s.nodes, 0, "discarded node should have been removed")
}

func TestResizePool_NoActionWhenQueueEmpty(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	s := New(prov, config)

	// No tasks, one existing queued node
	s.nodesQueue = []*nodeState{
		makeQueuedNode(s, "node-1", time.Now().Add(time.Minute)),
	}

	s.resizePool()

	// Queue should be emptied, no new nodes
	assert.Len(t, s.nodes, 0)
	assert.Len(t, s.nodesQueue, 0, "queue should be emptied when no tasks are queued")
}

func TestResizePool_NoNewNodesWhenAtMaxNodes(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 2
	s := New(prov, config)

	// Already at MaxNodes with 2 online nodes
	ns1, _ := makeOnlineNode(s, "node-1")
	ns2, _ := makeOnlineNode(s, "node-2")
	// Assign a running task to each so they don't get terminated
	dummyTask := &Task{Job: makeJob("j", "t"), Name: "t", Slots: 1, Log: silentLogger}
	ns1.tasks[0] = dummyTask
	ns2.tasks[0] = dummyTask
	s.nodes = []*nodeState{ns1, ns2}

	// Queue more tasks
	job := makeJob("job-x", "t1", "t2", "t3")
	s.tasksQueue = makeTasksForJob(job)

	initialNodes := len(s.nodes)
	s.resizePool()

	assert.Equal(t, initialNodes, len(s.nodes), "no new nodes should be created at MaxNodes")
	assert.Len(t, s.nodesQueue, 0, "queue should be empty at MaxNodes")
}

func TestResizePool_ProvisioningDelay(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 10
	config.SlotsPerNode = 1
	config.ProvisioningDelay = 30 * time.Second
	s := New(prov, config)

	// Queue 4 tasks
	job := makeJob("job-a", "a1", "a2", "a3", "a4")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	allNodes := append(s.nodes, s.nodesQueue...)
	assert.GreaterOrEqual(t, len(allNodes), 2, "should have created multiple nodes")

	// At most 1 node should be provisioning (the first one), rest queued
	provisioningCount := 0
	for _, ns := range s.nodes {
		if ns.status == NodeStatusProvisioning {
			provisioningCount++
		}
	}
	// First node created doesn't get queued, so exactly 1 provisioning
	assert.Equal(t, 1, provisioningCount,
		"only 1 node should be provisioning immediately (provisioning delay applies)")
}

func TestResizePool_ExcessQueuedNodesPruned(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 10
	s := New(prov, config)

	// Create a provisioning node (already covering 1 task)
	provNode := &nodeState{
		scheduler: s,
		status:    NodeStatusProvisioning,
		tasks:     make([]*Task, 1),
		log:       silentLogger,
		nodeName:  "prov-node",
	}
	s.nodes = []*nodeState{provNode}

	// Also have 2 queued nodes (from a previous tick when more tasks were queued)
	future := time.Now().Add(10 * time.Minute)
	s.nodesQueue = []*nodeState{
		makeQueuedNode(s, "queued-1", future),
		makeQueuedNode(s, "queued-2", future),
	}

	// But only 1 task remains
	job := makeJob("job-a", "task-1")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// Provisioning (1 node) covers 1 task. We have 1 task, so 0 more needed.
	// The 2 excess queued nodes should be pruned.
	assert.Equal(t, 0, len(s.nodesQueue), "excess queued nodes should be pruned")
}

func TestResizePool_QueuePrunedWhenProvisioningCoversNeeds(t *testing.T) {
	prov := &mockProvisioner{shutdownCh: make(chan struct{})}
	config := newTestConfig()
	config.MaxNodes = 10
	config.SlotsPerNode = 2
	s := New(prov, config)

	// 2 provisioning nodes (capacity = 2 * 2 = 4 slots)
	for i := 0; i < 2; i++ {
		s.nodes = append(s.nodes, &nodeState{
			scheduler: s,
			status:    NodeStatusProvisioning,
			tasks:     make([]*Task, 2),
			log:       silentLogger,
			nodeName:  fmt.Sprintf("prov-%d", i),
		})
	}

	// 3 queued nodes (leftover from a previous burst)
	future := time.Now().Add(10 * time.Minute)
	for i := 0; i < 3; i++ {
		s.nodesQueue = append(s.nodesQueue, makeQueuedNode(s, fmt.Sprintf("queued-%d", i), future))
	}

	// Only 3 tasks (3 slots needed, 4 slots incoming from provisioning)
	job := makeJob("job-a", "t1", "t2", "t3")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// Provisioning alone covers needs → all queued nodes should be removed
	assert.Equal(t, 0, len(s.nodesQueue), "queued nodes should be pruned when provisioning covers needs")
}

// --- Full lifecycle tests ---

func TestLifecycle_NodeReuse(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 1
	config.SlotsPerNode = 1
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Schedule 3 tasks with slotsPerNode=1 and MaxNodes=1 → all must run on the same node sequentially
	job := newTestJob("t1", "t2", "t3")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	events := collectEventsUntil(ch, 15*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	completedTasks := 0
	for _, e := range events {
		if _, ok := e.(EventTaskCompleted); ok {
			completedTasks++
		}
	}
	assert.Equal(t, 3, completedTasks, "all 3 tasks should complete via node reuse")
}

func TestLifecycle_MixedSlotWorkload(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 4
	config.SlotsPerNode = 4
	config.ProvisioningDelay = 0
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Mix of big and small tasks
	job := &Job{Job: &proto.Job{Name: "mixed", Tasks: []*proto.Job_Task{
		{Name: "big", Slots: 3},
		{Name: "small-1", Slots: 1},
		{Name: "small-2", Slots: 1},
	}}}
	_, err := s.Schedule(job)
	require.NoError(t, err)

	events := collectEventsUntil(ch, 10*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	hasJobCompleted := false
	for _, e := range events {
		if _, ok := e.(EventJobCompleted); ok {
			hasJobCompleted = true
		}
	}
	assert.True(t, hasJobCompleted, "job with mixed slot sizes should complete")
}
