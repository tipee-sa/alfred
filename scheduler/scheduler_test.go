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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock provisioner ---

type mockProvisioner struct {
	provisionFunc func(nodeName string, flavor string) (Node, error)
	shutdownOnce  sync.Once
	shutdownCh    chan struct{}

	mu         sync.Mutex
	provisions []provisionCall
}

type provisionCall struct {
	Name   string
	Flavor string
}

func newMockProvisioner() *mockProvisioner {
	return &mockProvisioner{
		shutdownCh: make(chan struct{}),
	}
}

func (p *mockProvisioner) Provision(nodeName string, flavor string) (Node, error) {
	p.mu.Lock()
	p.provisions = append(p.provisions, provisionCall{Name: nodeName, Flavor: flavor})
	p.mu.Unlock()

	if p.provisionFunc != nil {
		return p.provisionFunc(nodeName, flavor)
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

func (p *mockProvisioner) getProvisions() []provisionCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]provisionCall, len(p.provisions))
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
		DefaultFlavor:               "default",
		DefaultTasksPerNode:         1,
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
	return &Job{Job: &proto.Job{Name: "test", Tasks: tasks}}
}

func newTestJobWithFlavor(name string, flavor string, tasksPerNode uint32, tasks ...string) *Job {
	return &Job{
		Job: &proto.Job{
			Name:         name,
			Tasks:        tasks,
			Flavor:       flavor,
			TasksPerNode: tasksPerNode,
		},
	}
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	config.DefaultTasksPerNode = 2 // 2 slots per node
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
		n := &mockNode{
			name:        nodeName,
			taskDone:    make(chan struct{}),
			terminateCh: make(chan struct{}),
		}
		nodes <- n
		return n, nil
	}

	config := newTestConfig()
	config.DefaultTasksPerNode = 2
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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

func TestShutdownDrainsQueuedTasks(t *testing.T) {
	nodes := make(chan *mockNode, 10)

	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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

// --- Flavor / tasks-per-node tests ---
// These use instant-completing mockNodes (via default provisionFunc) since
// they test scheduling logic, not async task lifecycle.

// instantMockProvisioner returns a provisioner whose nodes complete tasks immediately.
func instantMockProvisioner() *mockProvisioner {
	prov := newMockProvisioner()
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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

func TestDefaultsApplied(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.DefaultFlavor = "my-default"
	config.DefaultTasksPerNode = 3
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("test-job", "", 0, "task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	assert.Equal(t, "my-default", job.Flavor)
	assert.Equal(t, uint32(3), job.TasksPerNode)

	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})
}

func TestFlavorMatching(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("flavored-job", "a16", 1, "task-x")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	provisions := prov.getProvisions()
	require.Len(t, provisions, 1)
	assert.Equal(t, "a16", provisions[0].Flavor)
}

func TestTasksPerNodeSlotCount(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("single-slot", "flavor-a", 1, "task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	events := collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	for _, e := range events {
		if nc, ok := e.(EventNodeCreated); ok {
			assert.Equal(t, 1, nc.NumSlots)
			break
		}
	}
}

func TestMixedWorkload(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.DefaultTasksPerNode = 2
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	jobA := newTestJobWithFlavor("job-a", "flavor-small", 1, "task-1")
	jobB := newTestJobWithFlavor("job-b", "flavor-big", 2, "task-2")

	_, err := s.Schedule(jobA)
	require.NoError(t, err)
	_, err = s.Schedule(jobB)
	require.NoError(t, err)

	completedJobs := 0
	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		if _, ok := e.(EventJobCompleted); ok {
			completedJobs++
		}
		return completedJobs >= 2
	})

	assert.Equal(t, 2, completedJobs, "expected 2 job completions")

	flavors := make(map[string]bool)
	for _, p := range prov.getProvisions() {
		flavors[p.Flavor] = true
	}
	assert.True(t, flavors["flavor-small"], "expected flavor-small to be provisioned")
	assert.True(t, flavors["flavor-big"], "expected flavor-big to be provisioned")
}

func TestJobWithExplicitValuesOverridesDefaults(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	config.DefaultFlavor = "default-f"
	config.DefaultTasksPerNode = 4
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("override", "custom-f", 2, "task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	assert.Equal(t, "custom-f", job.Flavor)
	assert.Equal(t, uint32(2), job.TasksPerNode)

	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	provisions := prov.getProvisions()
	require.Len(t, provisions, 1)
	assert.Equal(t, "custom-f", provisions[0].Flavor)
}

func TestProvisionFlavorPassthrough(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("test", "custom-flavor-42", 1, "task-a")
	_, err := s.Schedule(job)
	require.NoError(t, err)

	collectEventsUntil(ch, 5*time.Second, func(e Event) bool {
		_, ok := e.(EventJobCompleted)
		return ok
	})

	provisions := prov.getProvisions()
	require.Len(t, provisions, 1)
	assert.Equal(t, "custom-flavor-42", provisions[0].Flavor)
}

func TestIdleNodeTermination(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("test", "flavor-a", 1, "task-a")
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

func TestNodeSlotUpdatedHasCorrectJobName(t *testing.T) {
	prov := instantMockProvisioner()
	config := newTestConfig()
	s := New(prov, config)
	ch, unsub := s.Subscribe()
	defer unsub()

	go s.Run()
	defer s.Shutdown()

	job := newTestJobWithFlavor("my-job", "f", 1, "task-a")
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

	job := newTestJobWithFlavor("big-job", "flavor-a", 1, "t1", "t2", "t3", "t4", "t5")
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
	prov.provisionFunc = func(nodeName string, flavor string) (Node, error) {
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
