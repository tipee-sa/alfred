package scheduler

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock provisioner ---

type mockProvisioner struct {
	provisionFunc func(nodeName string) (Node, error)
	shutdownOnce  sync.Once
	shutdownCh    chan struct{}
}

func newMockProvisioner() *mockProvisioner {
	return &mockProvisioner{
		shutdownCh: make(chan struct{}),
	}
}

func (p *mockProvisioner) Provision(nodeName string) (Node, error) {
	if p.provisionFunc != nil {
		return p.provisionFunc(nodeName)
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

// --- Mock node ---

type mockNode struct {
	name string
	// Close taskDone to unblock RunTask
	taskDone chan struct{}
	// Close terminateCh to unblock Terminate
	terminateCh chan struct{}
}

func (n *mockNode) Name() string { return n.name }

func (n *mockNode) RunTask(_ *Task, _ RunTaskConfig) (int, error) {
	<-n.taskDone
	return 0, nil
}

func (n *mockNode) Terminate() error {
	<-n.terminateCh
	return nil
}

// --- Helpers ---

func newTestScheduler(prov Provisioner) *Scheduler {
	return New(prov, Config{
		Logger:                      slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
		MaxNodes:                    4,
		TasksPerNode:                1,
		ProvisioningDelay:           0,
		ProvisioningFailureCooldown: 0,
	})
}

func newTestJob(tasks ...string) *Job {
	return &Job{Job: &proto.Job{Name: "test", Tasks: tasks}}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- Tests ---

// TestScheduleDuringShutdown verifies that Schedule() returns an error (instead of
// deadlocking) when called after the scheduler has shut down. This exercises the
// select on s.stop in Schedule().
func TestScheduleDuringShutdown(t *testing.T) {
	prov := newMockProvisioner()
	s := newTestScheduler(prov)

	go s.Run()

	// Shut down the scheduler — Run() exits, nobody reads s.input anymore.
	s.Shutdown()
	s.Wait()

	// Schedule should return an error, not block forever.
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
// on nodeState.tasks (bug 2). It schedules 2 tasks on a 2-slot node so both tasks run
// concurrently on the same node, then completes them both to trigger concurrent writes.
func TestConcurrentTaskCompletion(t *testing.T) {
	// Track mock nodes so we can control task completion.
	// Each RunTask call gets its own mockNode (via perTaskNodes), so we can control
	// each task's completion independently.
	var mu sync.Mutex
	taskNodes := make(map[string]*mockNode)
	nodeReady := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}), // not used — overridden per-task below
			terminateCh: make(chan struct{}),
		}
		// Override RunTask to give each task its own control channel
		return &perTaskNode{
			mockNode:  n,
			mu:        &mu,
			taskNodes: taskNodes,
			ready:     nodeReady,
		}, nil
	}

	s := New(prov, Config{
		Logger:                      slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
		MaxNodes:                    4,
		TasksPerNode:                2, // 2 slots per node
		ProvisioningDelay:           0,
		ProvisioningFailureCooldown: 0,
	})

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// Schedule one job with 2 tasks — both will run on the same node
	_, err := s.Schedule(newTestJob("task-a", "task-b"))
	require.NoError(t, err)

	// Wait for both tasks to start running
	tn1 := waitForPerTaskNode(t, nodeReady)
	tn2 := waitForPerTaskNode(t, nodeReady)
	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	// Complete both tasks concurrently — this is the race condition trigger
	close(tn1.taskDone)
	close(tn2.taskDone)

	// Wait for job completion
	waitForEvent[EventJobCompleted](t, events)

	// Allow node to terminate
	mu.Lock()
	// Get any task node to access the underlying mockNode for terminateCh
	for _, tn := range taskNodes {
		close(tn.terminateCh)
		break
	}
	mu.Unlock()
}

// TestNodeTerminationRace verifies that node termination doesn't race on s.nodes (bug 1).
// It completes tasks so nodes become idle, triggering termination, then verifies new
// scheduling still works.
func TestNodeTerminationRace(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := New(prov, Config{
		Logger:                      slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
		MaxNodes:                    4,
		TasksPerNode:                1,
		ProvisioningDelay:           0,
		ProvisioningFailureCooldown: 0,
	})

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	// Schedule and complete a task, triggering node termination
	_, err := s.Schedule(newTestJob("task-1"))
	require.NoError(t, err)

	node1 := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	// Complete the task — node becomes idle and resizePool triggers termination
	close(node1.taskDone)
	waitForEvent[EventTaskCompleted](t, events)

	// Allow the node to terminate
	close(node1.terminateCh)
	waitForEvent[EventNodeTerminated](t, events)

	// Now schedule another job — this should work without issues even after termination
	_, err = s.Schedule(newTestJob("task-2"))
	require.NoError(t, err)

	node2 := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	// Clean up
	close(node2.taskDone)
	waitForEvent[EventTaskCompleted](t, events)
	close(node2.terminateCh)
}

// perTaskNode wraps a mockNode but gives each RunTask call its own control channel,
// allowing tests to complete individual tasks independently.
type perTaskNode struct {
	mockNode  *mockNode
	mu        *sync.Mutex
	taskNodes map[string]*mockNode
	ready     chan *mockNode
}

func (n *perTaskNode) Name() string { return n.mockNode.name }

func (n *perTaskNode) RunTask(task *Task, config RunTaskConfig) (int, error) {
	tn := &mockNode{
		name:        n.mockNode.name,
		taskDone:    make(chan struct{}),
		terminateCh: n.mockNode.terminateCh,
	}
	n.mu.Lock()
	n.taskNodes[task.Name] = tn
	n.mu.Unlock()
	n.ready <- tn
	<-tn.taskDone
	return 0, nil
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

// TestSchedulerBasic is a simple smoke test to verify the scheduler can schedule and
// complete a single task end-to-end.
func TestSchedulerBasic(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string) (Node, error) {
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

// TestMultipleTasksSingleJob verifies that a job with multiple tasks gets all tasks
// scheduled and completed.
func TestMultipleTasksSingleJob(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	s := New(prov, Config{
		Logger:                      slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
		MaxNodes:                    4,
		TasksPerNode:                1,
		ProvisioningDelay:           0,
		ProvisioningFailureCooldown: 0,
	})

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.Schedule(newTestJob("task-a", "task-b"))
	require.NoError(t, err)

	// Two nodes should be provisioned (one task per node)
	n1 := waitForNode(t, nodes)
	n2 := waitForNode(t, nodes)

	// Both tasks should be running
	waitForEvent[EventTaskRunning](t, events)
	waitForEvent[EventTaskRunning](t, events)

	// Complete both
	close(n1.taskDone)
	close(n2.taskDone)

	waitForEvent[EventJobCompleted](t, events)

	// Allow nodes to terminate
	close(n1.terminateCh)
	close(n2.terminateCh)
}

// TestProvisioningFailure verifies that the scheduler handles provisioning failures
// gracefully without hanging.
func TestProvisioningFailure(t *testing.T) {
	callCount := 0
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string) (Node, error) {
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

	s := New(prov, Config{
		Logger:                      slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
		MaxNodes:                    4,
		TasksPerNode:                1,
		ProvisioningDelay:           0,
		ProvisioningFailureCooldown: 10 * time.Millisecond, // short for testing
	})

	events, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer func() {
		s.Shutdown()
		s.Wait()
	}()

	_, err := s.Schedule(newTestJob("task-1"))
	require.NoError(t, err)

	// After the first provisioning fails and the cooldown passes, the scheduler
	// should retry with a new node and eventually succeed.
	node := waitForNode(t, nodes)
	waitForEvent[EventTaskRunning](t, events)

	close(node.taskDone)
	waitForEvent[EventJobCompleted](t, events)
	close(node.terminateCh)
}
