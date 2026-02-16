package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock provisioner ---

type mockProvisioner struct {
	provisionFunc func(ctx context.Context, nodeName string) (Node, error)
	shutdownOnce  sync.Once
	shutdownCh    chan struct{}

	mu         sync.Mutex
	provisions []string
}

func newMockProvisioner() *mockProvisioner {
	return &mockProvisioner{
		shutdownCh: make(chan struct{}),
	}
}

func (p *mockProvisioner) Provision(ctx context.Context, nodeName string) (Node, error) {
	p.mu.Lock()
	p.provisions = append(p.provisions, nodeName)
	p.mu.Unlock()

	if p.provisionFunc != nil {
		return p.provisionFunc(ctx, nodeName)
	}
	return &mockNode{
		name:        nodeName,
		taskDone:    make(chan struct{}),
		terminateCh: make(chan struct{}),
	}, nil
}

func (p *mockProvisioner) Shutdown() {
	p.shutdownOnce.Do(func() { close(p.shutdownCh) })
}

func (p *mockProvisioner) Wait() {
	<-p.shutdownCh
}

func (p *mockProvisioner) getProvisions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, len(p.provisions))
	copy(result, p.provisions)
	return result
}

// --- Mock node ---

type mockNode struct {
	name string
	// Close taskDone to unblock RunTask
	taskDone chan struct{}
	// Close terminateCh to unblock Terminate
	terminateCh chan struct{}
}

func (n *mockNode) Name() string { return n.name }

func (n *mockNode) RunTask(ctx context.Context, _ *Task, _ RunTaskConfig) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-n.taskDone:
		return 0, nil
	}
}

func (n *mockNode) Terminate() error {
	<-n.terminateCh
	return nil
}

// --- Helpers ---

func newTestConfig() Config {
	return Config{
		ArtifactPreserver:           func(io.Reader, *Task) error { return nil },
		SlotsPerNode:                1,
		Logger:                      slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
		MaxNodes:                    4,
		ProvisioningDelay:           0,
		ProvisioningFailureCooldown: 0,
		SecretLoader:                func(string) ([]byte, error) { return nil, nil },
	}
}

func newTestScheduler(prov Provisioner) *Scheduler {
	return New(prov, newTestConfig())
}

func newTestJob(tasks ...string) *Job {
	return &Job{Job: &proto.Job{Name: "test", Tasks: lo.Map(tasks, func(name string, _ int) *proto.Job_Task {
		return &proto.Job_Task{Name: name}
	})}}
}

func newTestJobWithSlots(name string, tasks map[string]uint32) *Job {
	var jobTasks []*proto.Job_Task
	for taskName, slots := range tasks {
		jobTasks = append(jobTasks, &proto.Job_Task{Name: taskName, Slots: slots})
	}
	return &Job{Job: &proto.Job{Name: name, Tasks: jobTasks}}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- perTaskNode ---

// perTaskNode wraps a mockNode but gives each RunTask call its own control channel,
// allowing tests to complete individual tasks independently.
type perTaskNode struct {
	mockNode  *mockNode
	mu        *sync.Mutex
	taskNodes map[string]*mockNode
	ready     chan *mockNode
}

func (n *perTaskNode) Name() string { return n.mockNode.name }

func (n *perTaskNode) RunTask(ctx context.Context, task *Task, config RunTaskConfig) (int, error) {
	tn := &mockNode{
		name:        n.mockNode.name,
		taskDone:    make(chan struct{}),
		terminateCh: n.mockNode.terminateCh,
	}
	n.mu.Lock()
	n.taskNodes[task.Name] = tn
	n.mu.Unlock()
	n.ready <- tn
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-tn.taskDone:
		return 0, nil
	}
}

func (n *perTaskNode) Terminate() error {
	<-n.mockNode.terminateCh
	return nil
}

// --- Test helpers ---

func waitForPerTaskNode(t *testing.T, nodes <-chan *mockNode) *mockNode {
	t.Helper()
	select {
	case n := <-nodes:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for per-task node")
		return nil
	}
}

func waitForNode(t *testing.T, nodes <-chan *mockNode) *mockNode {
	t.Helper()
	select {
	case n := <-nodes:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for node to be provisioned")
		return nil
	}
}

func waitForEvent[T Event](t *testing.T, events <-chan Event) T {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if typed, ok := ev.(T); ok {
				return typed
			}
		case <-time.After(5 * time.Second):
			var zero T
			t.Fatalf("timed out waiting for event %T", zero)
			return zero
		}
	}
}

// collectEventsUntil collects events until predicate returns true or timeout.
func collectEventsUntil(ch <-chan Event, timeout time.Duration, predicate func(Event) bool) []Event {
	var events []Event
	deadline := time.After(timeout)
	for {
		select {
		case event := <-ch:
			events = append(events, event)
			if predicate(event) {
				return events
			}
		case <-deadline:
			return events
		}
	}
}

// --- Concurrency tests ---

// TestScheduleDuringShutdown verifies that Schedule() returns an error (instead of
// deadlocking) when called after the scheduler has shut down.
func TestScheduleDuringShutdown(t *testing.T) {
	prov := newMockProvisioner()
	s := newTestScheduler(prov)

	go s.Run()

	s.Shutdown()
	s.Wait()

	done := make(chan struct{})
	var err error
	go func() {
		_, err = s.Schedule(newTestJob("task-1"))
		close(done)
	}()

	select {
	case <-done:
		assert.Error(t, err, "Schedule() should return an error after shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("Schedule() deadlocked after shutdown")
	}
}

// TestConcurrentTaskCompletion verifies that concurrent task completions don't race
// on nodeState.tasks.
func TestConcurrentTaskCompletion(t *testing.T) {
	var mu sync.Mutex
	taskNodes := make(map[string]*mockNode)
	nodeReady := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		return &perTaskNode{
			mockNode:  n,
			mu:        &mu,
			taskNodes: taskNodes,
			ready:     nodeReady,
		}, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 2 // 2 slots per node
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.Schedule(newTestJob("task-a", "task-b"))
	require.NoError(t, err)

	tn1 := waitForPerTaskNode(t, nodeReady)
	tn2 := waitForPerTaskNode(t, nodeReady)
	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	close(tn1.taskDone)
	close(tn2.taskDone)

	waitForEvent[EventJobCompleted](t, events)

	mu.Lock()
	for _, tn := range taskNodes {
		close(tn.terminateCh)
		break
	}
	mu.Unlock()
}

// TestNodeTerminationRace verifies that node termination doesn't race on s.nodes.
func TestNodeTerminationRace(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := newTestScheduler(prov)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.Schedule(newTestJob("task-1"))
	require.NoError(t, err)

	node1 := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	close(node1.taskDone)
	waitForEvent[EventTaskCompleted](t, events)

	close(node1.terminateCh)
	waitForEvent[EventNodeTerminated](t, events)

	_, err = s.Schedule(newTestJob("task-2"))
	require.NoError(t, err)

	node2 := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	close(node2.taskDone)
	waitForEvent[EventTaskCompleted](t, events)
	close(node2.terminateCh)
}

// --- Basic tests ---

func TestSchedulerBasic(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := newTestScheduler(prov)
	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()

	fqn, err := s.Schedule(newTestJob("my-task"))
	require.NoError(t, err)
	assert.Contains(t, fqn, "test-")

	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	close(node.taskDone)
	waitForEvent[EventJobCompleted](t, events)

	close(node.terminateCh)
	waitForEvent[EventNodeTerminated](t, events)

	s.Shutdown()
	s.Wait()
}

func TestMultipleTasksSingleJob(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := newTestScheduler(prov)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.Schedule(newTestJob("task-a", "task-b"))
	require.NoError(t, err)

	n1 := waitForNode(t, nodes)
	n2 := waitForNode(t, nodes)

	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	close(n1.taskDone)
	close(n2.taskDone)

	waitForEvent[EventJobCompleted](t, events)

	close(n1.terminateCh)
	close(n2.terminateCh)
}

func TestProvisioningFailure(t *testing.T) {
	callCount := 0
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		callCount++
		if callCount == 1 {
			return nil, fmt.Errorf("provisioning failed")
		}
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.ProvisioningFailureCooldown = 10 * time.Millisecond
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.Schedule(newTestJob("task-1"))
	require.NoError(t, err)

	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	close(node.taskDone)
	waitForEvent[EventJobCompleted](t, events)
	close(node.terminateCh)
}

// --- Cancel tests ---

func TestCancelRunningTask(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := newTestScheduler(prov)
	events, unsub := s.Subscribe()
	defer unsub()
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	job := newTestJob("task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	err = s.CancelTask(job.FQN(), "task-a")
	require.NoError(t, err)

	ev := waitForEvent[EventTaskAborted](t, events)
	assert.Equal(t, job.FQN(), ev.Job)
	assert.Equal(t, "task-a", ev.Task)

	waitForEvent[EventJobCompleted](t, events)
	close(node.terminateCh)
}

func TestCancelQueuedTask(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.MaxNodes = 1
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	job := newTestJob("task-a", "task-b")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	err = s.CancelTask(job.FQN(), "task-b")
	require.NoError(t, err)

	ev := waitForEvent[EventTaskAborted](t, events)
	assert.Equal(t, "task-b", ev.Task)

	err = s.CancelTask(job.FQN(), "task-a")
	require.NoError(t, err)

	waitForEvent[EventJobCompleted](t, events)
	close(node.terminateCh)
}

func TestCancelJob(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 2
	config.MaxNodes = 1
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	job := newTestJob("task-a", "task-b")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	node := waitForNode(t, nodes)

	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	err = s.CancelJob(job.FQN())
	require.NoError(t, err)

	aborted := map[string]bool{}
	for i := 0; i < 2; i++ {
		ev := waitForEvent[EventTaskAborted](t, events)
		aborted[ev.Task] = true
	}
	assert.True(t, aborted["task-a"])
	assert.True(t, aborted["task-b"])

	waitForEvent[EventJobCompleted](t, events)
	close(node.terminateCh)
}

func TestShutdownCancelsRunningTasks(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := newTestScheduler(prov)
	events, unsub := s.Subscribe()
	defer unsub()
	go s.Run()

	job := newTestJob("task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	s.Shutdown()

	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return within 3s after Shutdown()")
	}
}

func TestShutdownCancelsProvisioningContext(t *testing.T) {
	provisionStarted := make(chan struct{})

	prov := newMockProvisioner()
	prov.provisionFunc = func(ctx context.Context, nodeName string) (Node, error) {
		close(provisionStarted)
		// Block until context is cancelled — simulates a slow OpenStack SSH retry loop
		<-ctx.Done()
		return nil, ctx.Err()
	}

	s := newTestScheduler(prov)
	go s.Run()

	_, err := s.Schedule(newTestJob("task-a"))
	require.NoError(t, err)

	// Wait for Provision() to be called
	select {
	case <-provisionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Provision() was never called")
	}

	// Shutdown should cancel the provisioning context, unblocking Provision()
	s.Shutdown()

	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Shutdown completed promptly — provisioning context was cancelled
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() blocked — provisioning context was not cancelled on shutdown")
	}
}

func TestShutdownDrainsQueuedTasks(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.MaxNodes = 1
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()
	go s.Run()

	_, err := s.Schedule(newTestJob("task-a", "task-b"))
	require.NoError(t, err)

	waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	s.Shutdown()

	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() blocked — queued tasks were not drained on shutdown")
	}
}

func TestCancelQueuedTaskFreesResources(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.MaxNodes = 1
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	job := newTestJob("task-a", "task-b")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	err = s.CancelTask(job.FQN(), "task-b")
	require.NoError(t, err)
	waitForEvent[EventTaskAborted](t, events)

	err = s.CancelTask(job.FQN(), "task-a")
	require.NoError(t, err)
	waitForEvent[EventTaskAborted](t, events)

	waitForEvent[EventJobCompleted](t, events)

	close(node.terminateCh)
	waitForEvent[EventNodeTerminated](t, events)
}

func TestCancelNonexistentTask(t *testing.T) {
	prov := newMockProvisioner()
	s := newTestScheduler(prov)
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	err := s.CancelTask("nonexistent-job", "nonexistent-task")
	assert.Error(t, err)
}

// --- Slot-based tests ---

// instantMockProvisioner returns a provisioner whose nodes complete tasks immediately.
func instantMockProvisioner() *mockProvisioner {
	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		return &instantMockNode{name: nodeName}, nil
	}
	return prov
}

type instantMockNode struct{ name string }

func (n *instantMockNode) Name() string { return n.name }
func (n *instantMockNode) RunTask(_ context.Context, _ *Task, _ RunTaskConfig) (int, error) {
	return 0, nil
}
func (n *instantMockNode) Terminate() error { return nil }

func TestNodeSlotUpdatedHasCorrectJobName(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := &Job{Job: &proto.Job{Name: "my-job", Tasks: []*proto.Job_Task{{Name: "task-a"}}}}
	_, err := s.Schedule(job)
	require.NoError(t, err)

	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	for _, e := range events {
		if su, ok := e.(EventNodeSlotUpdated); ok && su.Task != nil {
			assert.Equal(t, "my-job", su.Task.Job)
			assert.Equal(t, "task-a", su.Task.Name)
			break
		}
	}
}

func TestMaxNodesGlobalCap(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.MaxNodes = 2
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJob("t1", "t2", "t3", "t4", "t5")
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
			break
		}
	}
	assert.True(t, hasJobCompleted, "job should complete despite MaxNodes cap")
}

func TestMultiSlotTaskAssignment(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.SlotsPerNode = 4
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Task requiring 3 slots on a node with 4 slots
	job := newTestJobWithSlots("test", map[string]uint32{"big-task": 3})
	_, err := s.Schedule(job)
	require.NoError(t, err)

	// Collect until node is terminated (slot frees happen after JobCompleted, before NodeTerminated)
	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventNodeTerminated)
		return ok
	})

	// Should see 3 slot updates (assign) + 3 slot updates (free) for the task
	slotAssigns := 0
	slotFrees := 0
	for _, e := range events {
		if su, ok := e.(EventNodeSlotUpdated); ok {
			if su.Task != nil {
				slotAssigns++
			} else {
				slotFrees++
			}
		}
	}
	assert.Equal(t, 3, slotAssigns, "expected 3 slot assignments for 3-slot task")
	assert.Equal(t, 3, slotFrees, "expected 3 slot frees for 3-slot task")
}

func TestMultiSlotTaskFreeing(t *testing.T) {
	var mu sync.Mutex
	taskNodes := make(map[string]*mockNode)
	nodeReady := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		return &perTaskNode{
			mockNode:  n,
			mu:        &mu,
			taskNodes: taskNodes,
			ready:     nodeReady,
		}, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 4
	config.MaxNodes = 1
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// Two tasks: one using 3 slots, one using 1 slot
	// The 1-slot task should only run after the 3-slot task frees its slots
	job := &Job{Job: &proto.Job{Name: "test", Tasks: []*proto.Job_Task{
		{Name: "big", Slots: 3},
		{Name: "small", Slots: 1},
	}}}
	_, err := s.Schedule(job)
	require.NoError(t, err)

	// big task starts first (3 slots)
	bigNode := waitForPerTaskNode(t, nodeReady)
	waitForEvent[EventTaskRunning](t, events)

	// small task can also fit (only 1 slot needed, 1 remaining)
	smallNode := waitForPerTaskNode(t, nodeReady)
	waitForEvent[EventTaskRunning](t, events)

	// Complete both
	close(bigNode.taskDone)
	close(smallNode.taskDone)

	waitForEvent[EventJobCompleted](t, events)

	mu.Lock()
	for _, tn := range taskNodes {
		close(tn.terminateCh)
		break
	}
	mu.Unlock()
}

func TestIdleNodeTermination(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJob("task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventNodeTerminated)
		return ok
	})

	hasTerminated := false
	for _, e := range events {
		if _, ok := e.(EventNodeTerminated); ok {
			hasTerminated = true
			break
		}
	}
	assert.True(t, hasTerminated, "expected node to be terminated after becoming idle")
}

// --- Live log reader tests ---

// callbackMockNode is a mock node that invokes RunTaskConfig callbacks,
// simulating what RunContainer does for workspace/log reader registration.
type callbackMockNode struct {
	name        string
	taskDone    chan struct{}
	terminateCh chan struct{}
}

func (n *callbackMockNode) Name() string { return n.name }

func (n *callbackMockNode) RunTask(ctx context.Context, _ *Task, config RunTaskConfig) (int, error) {
	// Simulate workspace setup: register live archiver and log reader
	if config.OnWorkspaceReady != nil {
		config.OnWorkspaceReady(func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("archive data")), nil
		})
	}
	if config.OnLogReaderReady != nil {
		config.OnLogReaderReady(func(lines int) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(fmt.Sprintf("log output (tail %d lines)\n", lines))), nil
		})
	}
	defer func() {
		if config.OnWorkspaceTeardown != nil {
			config.OnWorkspaceTeardown()
		}
	}()

	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-n.taskDone:
		return 0, nil
	}
}

func (n *callbackMockNode) Terminate() error {
	<-n.terminateCh
	return nil
}

func TestReadLiveTaskLogs(t *testing.T) {
	nodes := make(chan *callbackMockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &callbackMockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := newTestScheduler(prov)
	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	job := newTestJob("my-task")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	node := <-nodes
	ev := waitForEvent[EventTaskRunning](t, events)

	// Give callbacks a moment to register
	time.Sleep(50 * time.Millisecond)

	// Read live logs while task is running
	rc, err := s.ReadLiveTaskLogs(ev.Job, "my-task", 25)
	require.NoError(t, err)

	data, readErr := io.ReadAll(rc)
	require.NoError(t, readErr)
	require.NoError(t, rc.Close())

	assert.Equal(t, "log output (tail 25 lines)\n", string(data))

	// Complete the task
	close(node.taskDone)
	waitForEvent[EventJobCompleted](t, events)

	// Give teardown callback a moment to clean up
	time.Sleep(50 * time.Millisecond)

	// After task completion, live log reader should be unregistered
	_, err = s.ReadLiveTaskLogs(ev.Job, "my-task", 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no live log reader registered")

	close(node.terminateCh)
}

func TestReadLiveTaskLogsNotRunning(t *testing.T) {
	prov := newMockProvisioner()
	s := newTestScheduler(prov)
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.ReadLiveTaskLogs("nonexistent-job", "nonexistent-task", 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no live log reader registered")
}

// --- Slot validation and edge case tests ---

func TestScheduleRejectsTaskExceedingSlotsPerNode(t *testing.T) {
	prov := newMockProvisioner()
	config := newTestConfig()
	config.SlotsPerNode = 2
	s := New(prov, config)
	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// Task requiring 4 slots on a 2-slot node should be rejected
	job := newTestJobWithSlots("test", map[string]uint32{"huge-task": 4})
	_, err := s.Schedule(job)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires 4 slots but nodes only have 2")
}

func TestScheduleAcceptsTaskMatchingSlotsPerNode(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.SlotsPerNode = 4
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Task requiring exactly 4 slots on a 4-slot node should work
	job := newTestJobWithSlots("test", map[string]uint32{"exact-fit": 4})
	_, err := s.Schedule(job)
	require.NoError(t, err)

	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	hasJobCompleted := false
	for _, e := range events {
		if _, ok := e.(EventJobCompleted); ok {
			hasJobCompleted = true
		}
	}
	assert.True(t, hasJobCompleted, "task with slots == SlotsPerNode should complete")
}

func TestSlotsDefaultToOneWhenZero(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.SlotsPerNode = 1
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	// Job_Task with Slots=0 (proto default when not set) should default to 1
	job := &Job{Job: &proto.Job{Name: "test", Tasks: []*proto.Job_Task{
		{Name: "task-a", Slots: 0},
	}}}
	_, err := s.Schedule(job)
	require.NoError(t, err)

	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventNodeTerminated)
		return ok
	})

	// Verify exactly 1 slot was assigned and 1 freed
	slotAssigns := 0
	slotFrees := 0
	for _, e := range events {
		if su, ok := e.(EventNodeSlotUpdated); ok {
			if su.Task != nil {
				slotAssigns++
			} else {
				slotFrees++
			}
		}
	}
	assert.Equal(t, 1, slotAssigns, "Slots=0 should default to 1 slot assignment")
	assert.Equal(t, 1, slotFrees, "Slots=0 should default to 1 slot free")
}

func TestFragmentedSlotAssignment(t *testing.T) {
	var mu sync.Mutex
	taskNodes := make(map[string]*mockNode)
	nodeReady := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		return &perTaskNode{
			mockNode:  n,
			mu:        &mu,
			taskNodes: taskNodes,
			ready:     nodeReady,
		}, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 4
	config.MaxNodes = 1
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// First job: two 1-slot tasks fill slots [0] and [1], leaving [2] and [3] free
	job1 := &Job{Job: &proto.Job{Name: "job1", Tasks: []*proto.Job_Task{
		{Name: "a", Slots: 1},
		{Name: "b", Slots: 1},
	}}}
	_, err := s.Schedule(job1)
	require.NoError(t, err)

	tnA := waitForPerTaskNode(t, nodeReady)
	tnB := waitForPerTaskNode(t, nodeReady)
	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	// Complete task "a" — frees slot [0], leaving a fragmented node: [nil, b, nil, nil]
	close(tnA.taskDone)
	waitForEvent[EventTaskCompleted](t, events)

	// Now schedule a 2-slot task — it should fit in the remaining free slots
	job2 := &Job{Job: &proto.Job{Name: "job2", Tasks: []*proto.Job_Task{
		{Name: "big", Slots: 2},
	}}}
	_, err = s.Schedule(job2)
	require.NoError(t, err)

	// The 2-slot task should start (enough free slots: 3 free, need 2)
	waitForPerTaskNode(t, nodeReady)
	ev := waitForEvent[EventTaskRunning](t, events)
	assert.Equal(t, "big", ev.Task)

	// Clean up: complete remaining tasks
	close(tnB.taskDone)
	waitForEvent[EventTaskCompleted](t, events)

	mu.Lock()
	for name, tn := range taskNodes {
		if name == "big" {
			close(tn.taskDone)
		}
	}
	mu.Unlock()

	// Wait for all jobs to complete
	jobsCompleted := 0
	for jobsCompleted < 2 {
		waitForEvent[EventJobCompleted](t, events)
		jobsCompleted++
	}

	mu.Lock()
	for _, tn := range taskNodes {
		select {
		case <-tn.terminateCh:
		default:
			close(tn.terminateCh)
		}
		break
	}
	mu.Unlock()
}

// --- Task startup delay tests ---

func TestTaskStartupDelayStaggersTasks(t *testing.T) {
	var mu sync.Mutex
	taskNodes := make(map[string]*mockNode)
	nodeReady := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		return &perTaskNode{
			mockNode:  n,
			mu:        &mu,
			taskNodes: taskNodes,
			ready:     nodeReady,
		}, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 3
	config.MaxNodes = 1
	config.TaskStartupDelay = 100 * time.Millisecond
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// Schedule 3 tasks on a 3-slot node
	job := newTestJob("t1", "t2", "t3")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	// Collect EventTaskRunning timestamps
	var runningTimes []time.Time
	for i := 0; i < 3; i++ {
		waitForEvent[EventTaskRunning](t, events)
		runningTimes = append(runningTimes, time.Now())
	}

	// Verify tasks are spaced ~100ms apart (allow 50ms tolerance)
	for i := 1; i < len(runningTimes); i++ {
		gap := runningTimes[i].Sub(runningTimes[i-1])
		assert.GreaterOrEqual(t, gap.Milliseconds(), int64(50),
			"gap between task %d and %d should be >= 50ms, got %s", i-1, i, gap)
	}

	// Total spread should be >= 150ms (2 gaps of ~100ms)
	totalSpread := runningTimes[2].Sub(runningTimes[0])
	assert.GreaterOrEqual(t, totalSpread.Milliseconds(), int64(150),
		"total spread should be >= 150ms, got %s", totalSpread)

	// Clean up: complete all tasks
	for i := 0; i < 3; i++ {
		tn := waitForPerTaskNode(t, nodeReady)
		close(tn.taskDone)
	}
	waitForEvent[EventJobCompleted](t, events)

	mu.Lock()
	for _, tn := range taskNodes {
		close(tn.terminateCh)
		break
	}
	mu.Unlock()
}

func TestTaskStartupDelayCancelDuringDelay(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 2
	config.MaxNodes = 1
	config.TaskStartupDelay = 5 * time.Second // very long delay so we can cancel during it
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	job := newTestJob("task-a", "task-b")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	// Wait for first task to start running (no delay for first task)
	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	// task-b is assigned but waiting in startup delay (5s), cancel it now
	err = s.CancelTask(job.FQN(), "task-b")
	require.NoError(t, err)

	// task-b should be aborted without ever reaching EventTaskRunning
	ev := waitForEvent[EventTaskAborted](t, events)
	assert.Equal(t, "task-b", ev.Task)

	// Cancel task-a and clean up
	err = s.CancelTask(job.FQN(), "task-a")
	require.NoError(t, err)
	waitForEvent[EventTaskAborted](t, events)
	waitForEvent[EventJobCompleted](t, events)

	close(node.terminateCh)
}

func TestTaskStartupDelayShutdownDuringDelay(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 2
	config.MaxNodes = 1
	config.TaskStartupDelay = 5 * time.Second
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()

	_, err := s.Schedule(newTestJob("task-a", "task-b"))
	require.NoError(t, err)

	// task-a starts immediately, task-b is waiting in its 5s startup delay
	waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	// Shutdown while task-b is still in its delay
	s.Shutdown()

	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Wait() returned — both tasks were properly cleaned up
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() blocked — task in startup delay was not properly cleaned up on shutdown")
	}
}

func TestTaskStartupDelayNoDelayWhenZero(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.SlotsPerNode = 3
	config.TaskStartupDelay = 0 // explicitly disabled
	s := New(prov, config)

	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	_, err := s.Schedule(newTestJob("t1", "t2", "t3"))
	require.NoError(t, err)

	// All 3 tasks should complete quickly without any stagger
	events := collectEventsUntil(ch, 2*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	// Verify all 3 TaskRunning events happened (no tasks stuck waiting)
	runningCount := 0
	for _, e := range events {
		if _, ok := e.(EventTaskRunning); ok {
			runningCount++
		}
	}
	assert.Equal(t, 3, runningCount, "all 3 tasks should have started with no delay")
}

func TestConcurrentMultiSlotTaskFreeing(t *testing.T) {
	provisionedNodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(_ context.Context, nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		provisionedNodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.SlotsPerNode = 2
	config.MaxNodes = 2
	s := New(prov, config)

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// Two 2-slot tasks — each fills an entire node
	job := &Job{Job: &proto.Job{Name: "test", Tasks: []*proto.Job_Task{
		{Name: "big-a", Slots: 2},
		{Name: "big-b", Slots: 2},
	}}}
	_, err := s.Schedule(job)
	require.NoError(t, err)

	node1 := waitForNode(t, provisionedNodes)
	node2 := waitForNode(t, provisionedNodes)
	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	// Complete both simultaneously
	close(node1.taskDone)
	close(node2.taskDone)

	waitForEvent[EventJobCompleted](t, events)

	// Unblock termination so EventNodeTerminated can fire
	close(node1.terminateCh)
	close(node2.terminateCh)

	// Wait for both nodes to be terminated (slots properly freed)
	terminated := 0
	deadline := time.After(10 * time.Second)
	for terminated < 2 {
		select {
		case e := <-events:
			if _, ok := e.(EventNodeTerminated); ok {
				terminated++
			}
		case <-deadline:
			t.Fatalf("timed out waiting for node terminations, got %d/2", terminated)
		}
	}
}
