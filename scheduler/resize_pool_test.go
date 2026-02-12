package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
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

// failingProvisioner fails for specified flavors, succeeds for others.
type failingProvisioner struct {
	mu          sync.Mutex
	provisions  []provisionCall
	failFlavors map[string]error
}

func (p *failingProvisioner) Provision(nodeName string, flavor string) (Node, error) {
	p.mu.Lock()
	p.provisions = append(p.provisions, provisionCall{Name: nodeName, Flavor: flavor})
	p.mu.Unlock()

	if err, ok := p.failFlavors[flavor]; ok {
		return nil, err
	}
	return &instantMockNode{name: nodeName}, nil
}

func (p *failingProvisioner) Shutdown() {}
func (p *failingProvisioner) Wait()     {}

func (p *failingProvisioner) getProvisions() []provisionCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]provisionCall, len(p.provisions))
	copy(result, p.provisions)
	return result
}

// makeJob creates a Job with the given properties.
func makeJob(name, flavor string, tasksPerNode uint32, taskNames ...string) *Job {
	return &Job{
		Job: &proto.Job{
			Name:         name,
			Tasks:        taskNames,
			Flavor:       flavor,
			TasksPerNode: tasksPerNode,
		},
		id: "test",
	}
}

// makeTasksForJob creates Task objects for every task name in the job.
func makeTasksForJob(job *Job) []*Task {
	var tasks []*Task
	for _, name := range job.Tasks {
		tasks = append(tasks, &Task{
			Job:  job,
			Name: name,
			Log:  silentLogger,
		})
	}
	return tasks
}

// makeOnlineNode creates an online nodeState with the given type and a terminableNode.
func makeOnlineNode(s *Scheduler, name, flavor string, tasksPerNode int) (*nodeState, *terminableNode) {
	node := newTerminableNode(name)
	ns := &nodeState{
		scheduler:    s,
		node:         node,
		status:       NodeStatusOnline,
		flavor:       flavor,
		tasksPerNode: tasksPerNode,
		tasks:        make([]*Task, tasksPerNode),
		log:          silentLogger,
		nodeName:     name,
	}
	return ns, node
}

// makeQueuedNode creates a queued nodeState in the nodesQueue.
func makeQueuedNode(s *Scheduler, name, flavor string, tasksPerNode int, earliestStart time.Time) *nodeState {
	return &nodeState{
		scheduler:    s,
		status:       NodeStatusQueued,
		flavor:       flavor,
		tasksPerNode: tasksPerNode,
		tasks:        make([]*Task, tasksPerNode),
		log:          silentLogger,
		nodeName:     name,
		earliestStart: earliestStart,
	}
}

// countNodesOfType counts nodes in a slice matching the given type.
func countNodesOfType(nodes []*nodeState, flavor string, tasksPerNode int) int {
	count := 0
	for _, ns := range nodes {
		if ns.flavor == flavor && ns.tasksPerNode == tasksPerNode {
			count++
		}
	}
	return count
}

// --- Tests ---

func TestResizePool_IdleNodeTerminated_WhenNoMatchingTasks(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 10
	config.DefaultTasksPerNode = 1
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Schedule job with flavor "flavorA" — will create a flavorA node
	jobA := newTestJobWithFlavor("job-a", "flavorA", 1, "task-a")
	_, err := s.Schedule(jobA)
	require.NoError(t, err)

	// Wait for it to complete (node becomes idle)
	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	// Now schedule a job with DIFFERENT flavor — the idle flavorA node should be terminated
	jobB := newTestJobWithFlavor("job-b", "flavorB", 1, "task-b")
	_, err = s.Schedule(jobB)
	require.NoError(t, err)

	// Wait for the node termination event (the thing we're actually asserting on).
	// This may arrive before or after jobB completes, so don't stop at EventJobCompleted.
	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventNodeTerminated)
		return ok
	})

	// The flavorA node should have been terminated (no matching tasks)
	hasNodeTerminated := false
	for _, e := range events {
		if _, ok := e.(EventNodeTerminated); ok {
			hasNodeTerminated = true
		}
	}
	assert.True(t, hasNodeTerminated, "idle node of wrong type should be terminated")
}

func TestResizePool_IdleNodeKept_WhenMatchingTasksQueued(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 10
	s := New(prov, config)

	// Create an idle online node of type (flavorA, 2)
	ns, node := makeOnlineNode(s, "node-a", "flavorA", 2)
	s.nodes = append(s.nodes, ns)

	// Queue tasks of the SAME type (flavorA, 2)
	job := makeJob("job-a", "flavorA", 2, "task-1")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// The idle node should NOT be terminated (matching tasks exist)
	select {
	case <-node.terminated:
		t.Fatal("idle node with matching queued tasks should NOT have been terminated")
	case <-time.After(200 * time.Millisecond):
		// Good: node was kept
	}
}

func TestResizePool_CreatesNodesForMultipleTypes(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 10
	config.ProvisioningDelay = 0
	s := New(prov, config)

	// Queue tasks of two different types
	jobA := makeJob("job-a", "small", 1, "a1", "a2")
	jobB := makeJob("job-b", "big", 2, "b1", "b2")
	s.tasksQueue = append(makeTasksForJob(jobA), makeTasksForJob(jobB)...)

	s.resizePool()

	// Both types should have nodes created
	allNodes := append(s.nodes, s.nodesQueue...)
	smallCount := countNodesOfType(allNodes, "small", 1)
	bigCount := countNodesOfType(allNodes, "big", 2)

	assert.GreaterOrEqual(t, smallCount, 1, "expected at least 1 node for type (small, 1)")
	assert.GreaterOrEqual(t, bigCount, 1, "expected at least 1 node for type (big, 2)")
}

func TestResizePool_RespectsMaxNodesAcrossTypes(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 3
	config.ProvisioningDelay = 0
	s := New(prov, config)

	// Queue 5 tasks of type A (tpn=1, needs 5 nodes) and 5 of type B (tpn=1, needs 5 nodes)
	jobA := makeJob("job-a", "flavorA", 1, "a1", "a2", "a3", "a4", "a5")
	jobB := makeJob("job-b", "flavorB", 1, "b1", "b2", "b3", "b4", "b5")
	s.tasksQueue = append(makeTasksForJob(jobA), makeTasksForJob(jobB)...)

	s.resizePool()

	totalNodes := len(s.nodes) + len(s.nodesQueue)
	assert.LessOrEqual(t, totalNodes, config.MaxNodes,
		"total nodes (%d) should not exceed MaxNodes (%d)", totalNodes, config.MaxNodes)
}

func TestResizePool_DiscardedNodesRemoved(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	s := New(prov, config)

	// Create a discarded node
	ns := &nodeState{
		scheduler:    s,
		status:       NodeStatusDiscarded,
		flavor:       "f",
		tasksPerNode: 1,
		tasks:        make([]*Task, 1),
		log:          silentLogger,
		nodeName:     "dead-node",
	}
	s.nodes = append(s.nodes, ns)

	s.resizePool()

	assert.Len(t, s.nodes, 0, "discarded node should have been removed")
}

func TestResizePool_NodesQueuePruned_WhenNoDemandForType(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 10
	s := New(prov, config)

	// Put queued nodes of two types
	future := time.Now().Add(10 * time.Minute)
	s.nodesQueue = []*nodeState{
		makeQueuedNode(s, "node-a1", "flavorA", 1, future),
		makeQueuedNode(s, "node-a2", "flavorA", 1, future),
		makeQueuedNode(s, "node-b1", "flavorB", 2, future),
	}

	// Only queue tasks for type B → type A nodes should be pruned
	job := makeJob("job-b", "flavorB", 2, "task-1")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// Only type B queued nodes should remain
	for _, ns := range s.nodesQueue {
		assert.Equal(t, "flavorB", ns.flavor, "only flavorB nodes should remain in queue")
		assert.Equal(t, 2, ns.tasksPerNode, "only tpn=2 nodes should remain in queue")
	}
}

func TestResizePool_NoActionWhenQueueEmpty(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	s := New(prov, config)

	// No tasks, one existing queued node
	s.nodesQueue = []*nodeState{
		makeQueuedNode(s, "node-1", "f", 1, time.Now().Add(time.Minute)),
	}

	s.resizePool()

	// Queue should be emptied, no new nodes
	assert.Len(t, s.nodes, 0)
	assert.Len(t, s.nodesQueue, 0, "queue should be emptied when no tasks are queued")
}

func TestResizePool_NoNewNodesWhenAtMaxNodes(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 2
	s := New(prov, config)

	// Already at MaxNodes with 2 online nodes
	ns1, _ := makeOnlineNode(s, "node-1", "f", 1)
	ns2, _ := makeOnlineNode(s, "node-2", "f", 1)
	// Assign a running task to each so they don't get terminated
	dummyTask := &Task{Job: makeJob("j", "f", 1, "t"), Name: "t", Log: silentLogger}
	ns1.tasks[0] = dummyTask
	ns2.tasks[0] = dummyTask
	s.nodes = []*nodeState{ns1, ns2}

	// Queue more tasks
	job := makeJob("job-x", "f", 1, "t1", "t2", "t3")
	s.tasksQueue = makeTasksForJob(job)

	initialNodes := len(s.nodes)
	s.resizePool()

	assert.Equal(t, initialNodes, len(s.nodes), "no new nodes should be created at MaxNodes")
	assert.Len(t, s.nodesQueue, 0, "queue should be empty at MaxNodes")
}

func TestResizePool_ProvisioningDelaySharedAcrossTypes(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 10
	config.ProvisioningDelay = 30 * time.Second
	s := New(prov, config)

	// Queue tasks of two different types, both needing 2 nodes
	jobA := makeJob("job-a", "small", 1, "a1", "a2")
	jobB := makeJob("job-b", "big", 1, "b1", "b2")
	s.tasksQueue = append(makeTasksForJob(jobA), makeTasksForJob(jobB)...)

	s.resizePool()

	// With 30s provisioning delay, first node is immediate, rest are queued.
	// Total should be 4 nodes (2 per type) but most should be in the queue.
	allNodes := append(s.nodes, s.nodesQueue...)
	assert.GreaterOrEqual(t, len(allNodes), 2, "should have created nodes for both types")

	// At most 1 node should be provisioning (the first one), rest queued
	provisioningCount := 0
	for _, ns := range s.nodes {
		if ns.status == NodeStatusProvisioning {
			provisioningCount++
		}
	}
	// First node created doesn't get queued, so exactly 1 provisioning
	assert.Equal(t, 1, provisioningCount,
		"only 1 node should be provisioning immediately (provisioning delay applies globally)")
}

func TestResizePool_ExcessQueuedNodesOfTypePruned(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 10
	s := New(prov, config)

	// Create a provisioning node of type A (already covering 1 task)
	provNode := &nodeState{
		scheduler:    s,
		status:       NodeStatusProvisioning,
		flavor:       "flavorA",
		tasksPerNode: 1,
		tasks:        make([]*Task, 1),
		log:          silentLogger,
		nodeName:     "prov-node",
	}
	s.nodes = []*nodeState{provNode}

	// Also have 2 queued nodes of type A (from a previous tick when more tasks were queued)
	future := time.Now().Add(10 * time.Minute)
	s.nodesQueue = []*nodeState{
		makeQueuedNode(s, "queued-a1", "flavorA", 1, future),
		makeQueuedNode(s, "queued-a2", "flavorA", 1, future),
	}

	// But only 1 task of type A remains (the other completed)
	job := makeJob("job-a", "flavorA", 1, "task-1")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// Provisioning (1 node) covers 1 task. We have 1 task, so 0 more needed.
	// The 2 excess queued nodes should be pruned.
	queuedTypeA := countNodesOfType(s.nodesQueue, "flavorA", 1)
	assert.Equal(t, 0, queuedTypeA, "excess queued nodes of type A should be pruned")
}

func TestResizePool_QueuePrunedWhenProvisioningCoversNeeds(t *testing.T) {
	prov := &mockProvisioner{}
	config := newTestConfig()
	config.MaxNodes = 10
	s := New(prov, config)

	// 2 provisioning nodes of type A
	for i := 0; i < 2; i++ {
		s.nodes = append(s.nodes, &nodeState{
			scheduler:    s,
			status:       NodeStatusProvisioning,
			flavor:       "flavorA",
			tasksPerNode: 2,
			tasks:        make([]*Task, 2),
			log:          silentLogger,
			nodeName:     fmt.Sprintf("prov-%d", i),
		})
	}

	// 3 queued nodes of type A (leftover from a previous burst)
	future := time.Now().Add(10 * time.Minute)
	for i := 0; i < 3; i++ {
		s.nodesQueue = append(s.nodesQueue, makeQueuedNode(s, fmt.Sprintf("queued-%d", i), "flavorA", 2, future))
	}

	// Only 3 tasks of type A (2 provisioning nodes × 2 tpn = 4 capacity > 3 tasks)
	job := makeJob("job-a", "flavorA", 2, "t1", "t2", "t3")
	s.tasksQueue = makeTasksForJob(job)

	s.resizePool()

	// Provisioning alone covers needs → all queued nodes of type A should be removed
	queuedTypeA := countNodesOfType(s.nodesQueue, "flavorA", 2)
	assert.Equal(t, 0, queuedTypeA, "queued nodes should be pruned when provisioning covers needs")
}

// --- Full lifecycle tests ---

func TestLifecycle_ProvisioningFailure_OneType_OtherSucceeds(t *testing.T) {
	prov := &failingProvisioner{
		failFlavors: map[string]error{
			"bad-flavor": fmt.Errorf("flavor not found"),
		},
	}
	config := newTestConfig()
	config.DefaultTasksPerNode = 1
	config.ProvisioningFailureCooldown = 50 * time.Millisecond
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Schedule job with good flavor — should complete
	goodJob := newTestJobWithFlavor("good-job", "good-flavor", 1, "task-1")
	_, err := s.Schedule(goodJob)
	require.NoError(t, err)

	// Schedule job with bad flavor — will fail provisioning
	badJob := newTestJobWithFlavor("bad-job", "bad-flavor", 1, "task-2")
	_, err = s.Schedule(badJob)
	require.NoError(t, err)

	// The good job should complete
	events := collectEventsUntil(ch, 10*time.Second, func(e Event) bool {
		if ec, ok := e.(EventJobCompleted); ok {
			return ec.Job == goodJob.FQN()
		}
		return false
	})

	hasGoodJobCompleted := false
	for _, e := range events {
		if ec, ok := e.(EventJobCompleted); ok && ec.Job == goodJob.FQN() {
			hasGoodJobCompleted = true
		}
	}
	assert.True(t, hasGoodJobCompleted, "good job should complete despite bad flavor failing")
}

func TestLifecycle_NodeReuseWithinSameType(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 1
	config.DefaultTasksPerNode = 1
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Schedule 3 tasks with tpn=1 and MaxNodes=1 → all must run on the same node sequentially
	job := newTestJobWithFlavor("reuse-job", "flavor-x", 1, "t1", "t2", "t3")
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

	// Only 1 node should have been provisioned (reused for all 3 tasks)
	// Due to termination between reuses, there might be multiple provisions.
	// But all should be for the same flavor.
	for _, p := range prov.getProvisions() {
		assert.Equal(t, "flavor-x", p.Flavor)
	}
}

func TestLifecycle_TwoTypesRunConcurrently(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 4
	config.DefaultTasksPerNode = 1
	config.ProvisioningDelay = 0
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Schedule two jobs of different types concurrently
	jobA := newTestJobWithFlavor("type-a", "small", 1, "a1", "a2")
	jobB := newTestJobWithFlavor("type-b", "large", 2, "b1", "b2")

	_, err := s.Schedule(jobA)
	require.NoError(t, err)
	_, err = s.Schedule(jobB)
	require.NoError(t, err)

	// Wait for both to complete
	completedJobs := 0
	collectEventsUntil(ch, 10*time.Second, func(e Event) bool {
		if _, ok := e.(EventJobCompleted); ok {
			completedJobs++
		}
		return completedJobs >= 2
	})

	assert.Equal(t, 2, completedJobs, "both jobs should complete")

	// Verify provisioning: "small" nodes and "large" nodes were both created
	provisions := prov.getProvisions()
	flavorsProvisioned := make(map[string]int)
	for _, p := range provisions {
		flavorsProvisioned[p.Flavor]++
	}
	assert.Greater(t, flavorsProvisioned["small"], 0, "small flavor should have been provisioned")
	assert.Greater(t, flavorsProvisioned["large"], 0, "large flavor should have been provisioned")
}
