package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/gammadia/alfred/namegen"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/scheduler/internal"
	"github.com/samber/lo"
)

// Scheduler orchestrates task scheduling using a single-goroutine event loop (Run).
// All state mutations (tasksQueue, nodes, nodesQueue) happen exclusively in Run(),
// avoiding races. Background goroutines (watchTask/Node*) communicate back via
// channels and requestTick() — never by mutating scheduler state directly.
type Scheduler struct {
	provisioner Provisioner
	config      Config
	log         *slog.Logger

	// input receives jobs from Schedule(). Unbuffered: Schedule() blocks until Run() reads it.
	input chan *Job
	// tickRequests is buffered (capacity 1) to coalesce multiple scheduling requests into one.
	// Multiple goroutines call requestTick() concurrently; only one pending tick is needed.
	tickRequests chan any
	// deferred receives functions to execute on the main Run() goroutine after a delay.
	// Used by after() to safely mutate state from timer callbacks without races.
	deferred chan func()

	// tasksQueue and nodes are only accessed from Run() — no mutex needed.
	tasksQueue []*Task
	nodes      []*nodeState

	nodesQueue            []*nodeState
	earliestNextNodeStart time.Time

	// events is a buffered channel (capacity 64) feeding forwardEvents(), which distributes
	// to all subscribers. The buffer prevents broadcast() from blocking the main loop.
	events chan Event
	// listeners is the set of subscriber channels. Protected by RWMutex because
	// forwardEvents() reads it frequently while Subscribe/unsubscribe write infrequently.
	listeners      map[chan Event]bool
	listenersMutex sync.RWMutex

	// stop is closed (not sent to) by Shutdown() to unblock the Run() select.
	stop chan any

	// wg tracks all running tasks. Incremented in Schedule() (one per task),
	// decremented in watchTaskExecution() when each task finishes.
	// Wait() blocks on this to ensure graceful shutdown waits for all tasks.
	wg sync.WaitGroup

	// cancellations receives cancel requests from CancelJob/CancelTask.
	// Processed by the main Run() goroutine to safely mutate scheduler state.
	cancellations chan cancelRequest

	// taskCancels maps "jobFQN/taskName" to context.CancelFunc for running tasks.
	// Protected by taskCancelsMu because it's written from the main goroutine
	// (scheduleTaskOnOnlineNode, handleCancellation, shutdown) and from
	// watchTaskExecution (cleanup on task completion).
	taskCancels   map[string]context.CancelFunc
	taskCancelsMu sync.Mutex

	// liveArchivers allows downloading artifacts from still-running tasks.
	// Written by watchTaskExecution() callbacks, read by ArchiveLiveArtifact().
	liveArchivers   map[string]func() (io.ReadCloser, error)
	liveArchiversMu sync.RWMutex

	// liveLogReaders allows reading step logs from still-running tasks.
	// Written by watchTaskExecution() callbacks, read by ReadLiveTaskLogs().
	liveLogReaders   map[string]func(int) (io.ReadCloser, error)
	liveLogReadersMu sync.RWMutex
}

type cancelRequest struct {
	jobFQN   string
	taskName string // empty = cancel all tasks in the job
	done     chan error
}

func New(provisioner Provisioner, config Config) *Scheduler {
	scheduler := &Scheduler{
		provisioner: provisioner,
		config:      config,
		log:         config.Logger,

		input:        make(chan *Job),
		tickRequests: make(chan any, 1),
		deferred:     make(chan func()),

		earliestNextNodeStart: time.Now(),

		events:    make(chan Event, 64),
		listeners: make(map[chan Event]bool),

		stop: make(chan any),

		cancellations: make(chan cancelRequest),
		taskCancels:   make(map[string]context.CancelFunc),

		liveArchivers:  make(map[string]func() (io.ReadCloser, error)),
		liveLogReaders: make(map[string]func(int) (io.ReadCloser, error)),
	}

	scheduler.log.Debug("Scheduler config", "config", string(lo.Must(json.Marshal(config))))

	// forwardEvents runs as a long-lived goroutine that drains s.events and distributes
	// each event to all registered listeners (non-blocking: drops if a listener is full).
	// It exits when s.events is closed (in Shutdown).
	go scheduler.forwardEvents()

	return scheduler
}

// Subscribe returns a buffered event channel (capacity 1024) and an unsubscribe function.
// The caller must call the unsubscribe function when done to prevent memory leaks.
// Events are delivered best-effort: if the channel fills up, forwardEvents drops messages.
func (s *Scheduler) Subscribe() (<-chan Event, func()) {
	s.listenersMutex.Lock()
	defer s.listenersMutex.Unlock()

	channel := make(chan Event, 1024)
	s.listeners[channel] = true

	return channel, func() {
		s.listenersMutex.Lock()
		defer s.listenersMutex.Unlock()
		delete(s.listeners, channel)
	}
}

func (s *Scheduler) broadcast(event Event) {
	s.events <- event
}

func (s *Scheduler) forwardEvents() {
	emit := func(event Event) {
		s.listenersMutex.RLock()
		defer s.listenersMutex.RUnlock()

		for channel := range s.listeners {
			select {
			case channel <- event:
			default:
				s.log.Warn("Listener queue full, dropping event", "event", fmt.Sprintf("%T", event))
			}
		}
	}
	for event := range s.events {
		emit(event)
	}
}

// Schedule is called from gRPC handlers (any goroutine). It increments the WaitGroup
// for each task, then sends the job to the input channel. The send blocks until Run()
// reads it, which is fine — Run() is always selecting on input.
//
// Uses select on s.input and s.stop to avoid deadlocking when the scheduler is shutting
// down (Run() has exited, so nobody reads from s.input).
func (s *Scheduler) Schedule(job *Job) (string, error) {
	job.id = namegen.Get()
	nbTasks := len(job.Tasks)

	// Validate that every task fits within the node's slot capacity
	for _, task := range job.Tasks {
		slots := int(task.Slots)
		if slots < 1 {
			slots = 1
		}
		if slots > s.config.SlotsPerNode {
			s.log.Warn("Rejecting job: task exceeds node slot capacity",
				"job", job.FQN(), "task", task.Name, "taskSlots", slots, "slotsPerNode", s.config.SlotsPerNode)
			return "", fmt.Errorf("task '%s' requires %d slots but nodes only have %d (--slots-per-node)", task.Name, slots, s.config.SlotsPerNode)
		}
	}

	s.log.Info("Scheduling job", "name", job.FQN(), "tasks", nbTasks)
	s.wg.Add(nbTasks)

	select {
	case s.input <- job:
		return job.FQN(), nil
	case <-s.stop:
		s.wg.Add(-nbTasks) // undo the Add above
		return job.FQN(), fmt.Errorf("scheduler is shutting down, ignoring job '%s' (%d tasks)", job.Name, nbTasks)
	}
}

// Wait blocks until all tasks have finished (wg counter reaches 0) and the provisioner
// has cleaned up. Called by the server during shutdown to ensure graceful completion.
func (s *Scheduler) Wait() {
	s.log.Debug("Waiting for all tasks to finish")
	s.wg.Wait()
	s.log.Debug("All tasks finished, waiting for provisioner cleanup")
	s.provisioner.Wait()
	s.log.Debug("Provisioner cleanup complete")
}

// Shutdown signals the scheduler to stop. Closing s.stop unblocks the select in Run().
// A background goroutine waits for all tasks to finish, then closes s.events, which
// causes forwardEvents() to exit (range over closed channel returns).
func (s *Scheduler) Shutdown() {
	close(s.stop)
	go func() {
		s.Wait()
		close(s.events) // forwardEvents() exits when this channel is closed
	}()
}

// Run is the scheduler's main event loop. It runs in a single goroutine — all state
// mutations (tasksQueue, nodes, nodesQueue) happen here, so no mutexes are needed for them.
//
// The loop blocks on select until one of four things happens:
//   - A new job arrives (from Schedule via s.input)
//   - A tick is requested (from any goroutine via requestTick)
//   - A deferred function fires (from after() via s.deferred)
//   - Shutdown is signaled (from Shutdown() closing s.stop)
//
// Background goroutines (watchTaskExecution, watchNodeProvisioning, watchNodeTermination)
// communicate back to this loop only via requestTick() and broadcast() — never by
// modifying scheduler fields directly.
func (s *Scheduler) Run() {
	s.log.Info("Scheduler is running")

	for {
		select {
		// New job submitted: broadcast the job event, enqueue all its tasks, and trigger a scheduling pass.
		case job := <-s.input:
			taskNames := lo.Map(job.Tasks, func(t *proto.Job_Task, _ int) string { return t.Name })
			s.broadcast(EventJobScheduled{
				Job: job.FQN(), About: job.About, Tasks: taskNames,
				Jobfile: job.Jobfile, CommandLine: job.CommandLine, StartedBy: job.StartedBy,
			})
			for _, jobTask := range job.Tasks {
				slots := int(jobTask.Slots)
				if slots < 1 {
					slots = 1
				}
				task := &Task{
					Job:   job,
					Name:  jobTask.Name,
					Slots: slots,
					Log:   s.log.With(slog.Group("task", "job", job.FQN(), "name", jobTask.Name)),
				}

				task.Log.Debug("Queuing task", "slots", slots)
				s.broadcast(EventTaskQueued{Job: task.Job.FQN(), Task: task.Name})
				s.tasksQueue = append(s.tasksQueue, task)
			}
			s.requestTick("job tasks should be scheduled")

		// Scheduling pass: assign queued tasks to free node slots, then resize the node pool.
		// Multiple requestTick() calls coalesce into a single pass (channel capacity 1).
		case <-s.tickRequests:
			for len(s.tasksQueue) > 0 {
				if !s.scheduleTaskOnOnlineNode() {
					break // no free slots available
				}
			}
			s.resizePool()

		// Delayed function execution: after() uses time.AfterFunc to send functions here
		// so they run on this goroutine (safe to mutate scheduler state).
		case f := <-s.deferred:
			f()

		// Cancellation request: cancel queued and/or running tasks.
		case req := <-s.cancellations:
			s.handleCancellation(req)

		// Shutdown: stop accepting jobs, shut down provisioner, cancel all running tasks,
		// drain queued tasks, terminate all online nodes, then exit the loop.
		// The caller (server) waits via Wait().
		case <-s.stop:
			s.log.Info("Shutting down scheduler")
			s.provisioner.Shutdown()

			// Cancel all running tasks so Wait() returns promptly
			s.taskCancelsMu.Lock()
			for _, cancel := range s.taskCancels {
				cancel()
			}
			s.taskCancelsMu.Unlock()

			// Drain queued tasks that will never run: broadcast aborts and
			// decrement WaitGroup so Wait() doesn't block indefinitely.
			for _, task := range s.tasksQueue {
				task.Log.Warn("Aborting queued task due to scheduler shutdown")
				s.broadcast(EventTaskAborted{Job: task.Job.FQN(), Task: task.Name})
				if task.Job.tasksCompleted.Add(1) == uint32(len(task.Job.Tasks)) {
					s.broadcast(EventJobCompleted{Job: task.Job.FQN()})
				}
				s.wg.Done()
			}
			s.tasksQueue = nil

			s.log.Info("Terminating online nodes")
			for _, nodeState := range s.nodes {
				if nodeState.status == NodeStatusOnline {
					go nodeState.node.Terminate()
				}
			}
			return
		}
	}
}

// requestTick requests a tick to be performed as soon as possible
// If a tick is already scheduled, this function does nothing
// This function is safe to call from multiple goroutines
func (s *Scheduler) requestTick(intent string) {
	select {
	case s.tickRequests <- nil: // Yeah! 🎉
		s.log.Debug("Requested tick", "intent", intent)
	default: // No need to queue ticks!
	}
}

// after schedules a function to be executed after a delay on the main scheduler goroutine.
// This function is safe to call from multiple goroutines.
// If the scheduler shuts down before the timer fires, the function is silently dropped.
func (s *Scheduler) after(d time.Duration, f func()) {
	time.AfterFunc(d, func() {
		select {
		case s.deferred <- f:
		case <-s.stop:
		}
	})
}

func (s *Scheduler) handleCancellation(req cancelRequest) {
	found := false

	// Cancel queued tasks: remove from queue, broadcast abort, decrement WaitGroup
	remaining := s.tasksQueue[:0]
	for _, task := range s.tasksQueue {
		matches := task.Job.FQN() == req.jobFQN && (req.taskName == "" || task.Name == req.taskName)
		if matches {
			found = true
			task.Log.Info("Task aborted (was queued)")
			s.broadcast(EventTaskAborted{Job: task.Job.FQN(), Task: task.Name})
			if task.Job.tasksCompleted.Add(1) == uint32(len(task.Job.Tasks)) {
				s.log.Info("Job completed", "name", task.Job.FQN())
				s.broadcast(EventJobCompleted{Job: task.Job.FQN()})
			}
			s.wg.Done()
		} else {
			remaining = append(remaining, task)
		}
	}
	s.tasksQueue = remaining

	// Cancel running tasks: call their cancel funcs (context propagates to RunContainer)
	s.taskCancelsMu.Lock()
	for key, cancel := range s.taskCancels {
		// key format: "jobFQN/taskName"
		taskJobFQN, taskName, _ := splitTaskKey(key)
		if taskJobFQN == req.jobFQN && (req.taskName == "" || taskName == req.taskName) {
			found = true
			cancel()
		}
	}
	s.taskCancelsMu.Unlock()

	if !found {
		req.done <- fmt.Errorf("no matching tasks found for job %q task %q", req.jobFQN, req.taskName)
	} else {
		req.done <- nil
		s.requestTick("cancelled tasks should free resources")
	}
}

// CancelJob cancels all queued and running tasks for the given job.
func (s *Scheduler) CancelJob(jobFQN string) error {
	done := make(chan error, 1)
	select {
	case s.cancellations <- cancelRequest{jobFQN: jobFQN, done: done}:
		return <-done
	case <-s.stop:
		return fmt.Errorf("scheduler is shutting down")
	}
}

// CancelTask cancels a specific task within a job.
func (s *Scheduler) CancelTask(jobFQN string, taskName string) error {
	done := make(chan error, 1)
	select {
	case s.cancellations <- cancelRequest{jobFQN: jobFQN, taskName: taskName, done: done}:
		return <-done
	case <-s.stop:
		return fmt.Errorf("scheduler is shutting down")
	}
}

func splitTaskKey(key string) (jobFQN, taskName string, ok bool) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

func (s *Scheduler) scheduleTaskOnOnlineNode() bool {
	task := s.tasksQueue[0]

	for _, ns := range s.nodes {
		if ns.status != NodeStatusOnline {
			continue
		}

		// Count free slots (nil entries)
		freeSlots := 0
		for _, t := range ns.tasks {
			if t == nil {
				freeSlots++
			}
		}
		if freeSlots < task.Slots {
			task.Log.Debug("Skipping node: insufficient free slots", "node", ns.nodeName, "freeSlots", freeSlots, "needed", task.Slots)
			continue
		}

		// Assign the task pointer to the first task.Slots free slots
		assigned := 0
		for slot := range ns.tasks {
			if ns.tasks[slot] != nil {
				continue
			}
			ns.tasks[slot] = task
			s.broadcast(EventNodeSlotUpdated{Node: ns.nodeName, Slot: slot, Task: &NodeSlotTask{task.Job.Name, task.Name}})
			assigned++
			if assigned == task.Slots {
				break
			}
		}

		s.tasksQueue = s.tasksQueue[1:]

		// Stagger task starts to avoid overwhelming the node
		var startDelay time.Duration
		if s.config.TaskStartupDelay > 0 {
			now := time.Now()
			if ns.nextTaskStart.Before(now) {
				ns.nextTaskStart = now
			}
			startDelay = ns.nextTaskStart.Sub(now)
			ns.nextTaskStart = ns.nextTaskStart.Add(s.config.TaskStartupDelay)
		}

		task.Log.Info("Scheduling task on node", "node", ns.node.Name(), "slots", task.Slots, "startDelay", startDelay, "remainingTasks", len(s.tasksQueue))

		taskKey := task.Job.FQN() + "/" + task.Name
		ctx, cancel := context.WithCancel(context.Background())

		s.taskCancelsMu.Lock()
		s.taskCancels[taskKey] = cancel
		s.taskCancelsMu.Unlock()

		go s.watchTaskExecution(ctx, cancel, taskKey, ns, task, startDelay) // async: blocks on RunTask
		return true
	}

	return false
}

func (s *Scheduler) resizePool() {
	// Remove discarded nodes
	s.nodes = lo.Filter(s.nodes, func(nodeState *nodeState, _ int) bool {
		return nodeState.status != NodeStatusDiscarded
	})

	// Terminate idle online nodes when there are no queued tasks
	for _, ns := range s.nodes {
		if ns.status != NodeStatusOnline {
			continue
		}
		if !lo.EveryBy(ns.tasks, func(task *Task) bool { return task == nil }) {
			continue
		}
		if len(s.tasksQueue) == 0 {
			// Mark as terminating here (on the main goroutine) before spawning the
			// background goroutine, to avoid a data race on nodeState.status.
			ns.UpdateStatus(NodeStatusTerminating)
			go s.watchNodeTermination(ns) // async: blocks on node.Terminate()
		}
	}

	// No queued tasks or at global node cap → prune queue and return
	if len(s.tasksQueue) < 1 || len(s.nodes) >= s.config.MaxNodes {
		s.emptyNodesQueue()
		return
	}

	// Sum total slots needed across all queued tasks
	totalSlotsNeeded := 0
	for _, task := range s.tasksQueue {
		totalSlotsNeeded += task.Slots
	}

	slotsPerNode := s.config.SlotsPerNode

	// Count provisioning nodes
	provisioningNodes := 0
	for _, ns := range s.nodes {
		if ns.status == NodeStatusProvisioning {
			provisioningNodes++
		}
	}

	s.log.Debug("Need more nodes",
		"totalSlotsNeeded", totalSlotsNeeded,
		"slotsPerNode", slotsPerNode,
		"maxNodes", s.config.MaxNodes,
		"totalNodes", len(s.nodes),
	)

	nbNodesToCreate := func(incomingNodes int) int {
		return internal.NbNodesToCreate(s.config.MaxNodes, slotsPerNode, totalSlotsNeeded, len(s.nodes), incomingNodes)
	}

	nodesToCreate := 0

	// First, we check if provisioning nodes cover our needs
	if nodesToCreate = nbNodesToCreate(provisioningNodes); nodesToCreate < 1 {
		s.log.Debug("Provisioning nodes cover our needs",
			"needed", nodesToCreate, "provisioningNodes", provisioningNodes,
		)
		s.emptyNodesQueue()
		return
	}
	s.log.Debug("Provisioning nodes are not enough",
		"needed", nodesToCreate, "provisioningNodes", provisioningNodes,
	)

	// Then, we check if nodes in the queue that we can now provision cover our needs
	queuedNodes := 0
	provisioningQueueNodes := 0
	for _, ns := range s.nodesQueue {
		if time.Now().After(ns.earliestStart) {
			ns.log.Info("Provisioning node from the queue")
			ns.UpdateStatus(NodeStatusProvisioning)

			s.nodesQueue = lo.Without(s.nodesQueue, ns)
			s.nodes = append(s.nodes, ns)
			go s.watchNodeProvisioning(ns)

			provisioningQueueNodes++

			if nodesToCreate = nbNodesToCreate(provisioningNodes + provisioningQueueNodes); nodesToCreate < 1 {
				s.log.Debug("Provisioning nodes from the queue cover our needs",
					"needed", nodesToCreate,
					"provisioningNodes", provisioningNodes,
					"provisioningQueueNodes", provisioningQueueNodes,
				)
				s.emptyNodesQueue()
				return
			}
			s.log.Debug("Provisioning nodes from the queue are not enough",
				"needed", nodesToCreate,
				"provisioningNodes", provisioningNodes,
				"provisioningQueueNodes", provisioningQueueNodes,
			)
		} else {
			queuedNodes++
		}
	}

	// Finally, we check if nodes queued in the future cover our needs
	if nodesToCreate = nbNodesToCreate(provisioningNodes + provisioningQueueNodes + queuedNodes); nodesToCreate < 1 {
		s.log.Debug("Provisioning nodes and queued nodes cover our needs",
			"needed", nodesToCreate,
			"provisioningNodes", provisioningNodes,
			"provisioningQueueNodes", provisioningQueueNodes,
			"queuedNodes", queuedNodes,
		)
		if nodesToCreate < 0 {
			s.log.Debug("Remove extra queued nodes", "nodesToRemove", -nodesToCreate)
			s.removeQueuedNodes(-nodesToCreate)
		}
		return
	}
	s.log.Debug("Provisioning nodes and queued nodes are not enough",
		"needed", nodesToCreate,
		"provisioningNodes", provisioningNodes,
		"provisioningQueueNodes", provisioningQueueNodes,
		"queuedNodes", queuedNodes,
	)

	// Prevent ever starting a node "right now" because the earliest start time has already passed
	if s.earliestNextNodeStart.Before(time.Now()) {
		s.earliestNextNodeStart = time.Now()
	}

	for i := 0; i < nodesToCreate; i++ {
		// Respect global MaxNodes cap
		if len(s.nodes)+len(s.nodesQueue) >= s.config.MaxNodes {
			break
		}

		// Never queue the first node we create
		queueNode := (len(s.nodes) > 0 || len(s.nodesQueue) > 0 || i > 0) && time.Now().Before(s.earliestNextNodeStart)

		nodeName := namegen.Get()
		ns := &nodeState{
			scheduler: s,

			node:   nil,
			status: lo.Ternary(queueNode, NodeStatusQueued, NodeStatusProvisioning),
			tasks:  make([]*Task, slotsPerNode),
			log:    s.log.With("component", "node").With(slog.Group("node", "name", nodeName)),

			nodeName:      nodeName,
			earliestStart: s.earliestNextNodeStart,
		}

		ns.log.Debug("Creating node", "earliestStart", ns.earliestStart, "status", ns.status, "slotsPerNode", slotsPerNode)
		s.broadcast(EventNodeCreated{Node: nodeName, Status: ns.status, NumSlots: slotsPerNode})

		if queueNode {
			s.nodesQueue = append(s.nodesQueue, ns)

			wait := time.Until(s.earliestNextNodeStart)
			s.after(wait, func() {
				s.requestTick("queued node should be ready to be provisioned")
			})
		} else {
			s.nodes = append(s.nodes, ns)
			go s.watchNodeProvisioning(ns)
		}

		// The next node should start with a delay
		s.earliestNextNodeStart = s.earliestNextNodeStart.Add(s.config.ProvisioningDelay)
	}
}

// watchNodeProvisioning runs in its own goroutine (spawned from resizePool).
// It blocks on provisioner.Provision() which may take seconds (local Docker)
// or minutes (OpenStack VM creation + SSH connection).
// On success: marks node online and requests a tick so tasks can be assigned.
// On failure: schedules a deferred discard after ProvisioningFailureCooldown.
//
// State mutations (nodeState.node, nodeState.status) are sent to the main goroutine
// via the deferred channel to avoid data races with the main loop.
func (s *Scheduler) watchNodeProvisioning(nodeState *nodeState) {
	nodeState.log.Info("Provisioning node")

	if node, err := s.provisioner.Provision(nodeState.nodeName); err != nil {
		nodeState.log.Error("Provisioning of node failed", "error", err)

		select {
		case s.deferred <- func() {
			nodeState.UpdateStatus(NodeStatusFailedProvisioning)
			// Schedule cleanup on the main goroutine (via deferred channel) after cooldown
			s.after(s.config.ProvisioningFailureCooldown, func() {
				nodeState.log.Info("Discarding failed node")
				nodeState.UpdateStatus(NodeStatusDiscarded)
				s.requestTick("discarded node (because of failed provisioning) should be removed")
			})
		}:
		case <-s.stop:
		}
	} else {
		nodeState.log.Info("Node is online")

		select {
		case s.deferred <- func() {
			nodeState.node = node
			nodeState.UpdateStatus(NodeStatusOnline)
			s.requestTick("online node should be ready for duty")
		}:
		case <-s.stop:
		}
	}
}

func (s *Scheduler) ArchiveLiveArtifact(jobFQN, taskName string) (io.ReadCloser, error) {
	key := jobFQN + "/" + taskName

	s.liveArchiversMu.RLock()
	archiver, ok := s.liveArchivers[key]
	s.liveArchiversMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no live archiver registered for %q (task may not be running or workspace not ready yet)", key)
	}

	s.log.Debug("Invoking live archiver", "key", key)
	rc, err := archiver()
	if err != nil {
		return nil, fmt.Errorf("live archiver for %q failed: %w", key, err)
	}
	s.log.Debug("Live archiver returned reader successfully", "key", key)
	return rc, nil
}

func (s *Scheduler) ReadLiveTaskLogs(jobFQN, taskName string, lines int) (io.ReadCloser, error) {
	key := jobFQN + "/" + taskName

	s.liveLogReadersMu.RLock()
	logReader, ok := s.liveLogReaders[key]
	s.liveLogReadersMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no live log reader registered for %q (task may not be running or workspace not ready yet)", key)
	}

	s.log.Debug("Invoking live log reader", "key", key, "lines", lines)
	rc, err := logReader(lines)
	if err != nil {
		return nil, fmt.Errorf("live log reader for %q failed: %w", key, err)
	}
	s.log.Debug("Live log reader returned reader successfully", "key", key)
	return rc, nil
}

// watchTaskExecution runs in its own goroutine (one per task, spawned from scheduleTaskOnOnlineNode).
// It blocks on node.RunTask() for the entire duration of the task (which calls RunContainer
// internally — creating network, starting services, running steps, archiving artifacts).
//
// Lifecycle: (optional delay) → broadcast Running → block on RunTask → broadcast Aborted/Failed/Completed →
// atomic increment job counter (detect job completion) → free node slot → wg.Done → requestTick.
func (s *Scheduler) watchTaskExecution(ctx context.Context, cancel context.CancelFunc, taskKey string, nodeState *nodeState, task *Task, startDelay time.Duration) {
	defer func() {
		cancel()
		s.taskCancelsMu.Lock()
		delete(s.taskCancels, taskKey)
		s.taskCancelsMu.Unlock()
	}()

	// Stagger task starts to avoid overwhelming the node with concurrent Docker operations
	if startDelay > 0 {
		task.Log.Debug("Waiting before starting task", "delay", startDelay)
		select {
		case <-time.After(startDelay):
			task.Log.Debug("Startup delay elapsed, starting task now")
		case <-ctx.Done():
			// Task was cancelled while waiting to start
			task.Log.Info("Task aborted during startup delay")
			s.broadcast(EventTaskAborted{Job: task.Job.FQN(), Task: task.Name})

			if task.Job.tasksCompleted.Add(1) == uint32(len(task.Job.Tasks)) {
				s.log.Info("Job completed", "name", task.Job.FQN())
				s.broadcast(EventJobCompleted{Job: task.Job.FQN()})
			}

			select {
			case s.deferred <- func() {
				freed := 0
				for slot, t := range nodeState.tasks {
					if t == task {
						s.broadcast(EventNodeSlotUpdated{Node: nodeState.nodeName, Slot: slot, Task: nil})
						nodeState.tasks[slot] = nil
						freed++
					}
				}
				task.Log.Debug("Freed task slots", "node", nodeState.nodeName, "slotsFreed", freed)
				s.requestTick("aborted task during delay should give its slots to another task")
			}:
			case <-s.stop:
			}
			s.wg.Done()
			return
		}
	}

	node := nodeState.node

	key := taskKey
	runConfig := RunTaskConfig{
		ArtifactPreserver: s.config.ArtifactPreserver,
		SecretLoader:      s.config.SecretLoader,
		// Callbacks invoked by RunContainer when workspace is created/destroyed.
		// They register/unregister a live archiver so clients can download artifacts
		// from running tasks via ArchiveLiveArtifact().
		OnWorkspaceReady: func(archiver func() (io.ReadCloser, error)) {
			s.liveArchiversMu.Lock()
			defer s.liveArchiversMu.Unlock()
			s.liveArchivers[key] = archiver
		},
		OnLogReaderReady: func(logReader func(lines int) (io.ReadCloser, error)) {
			s.liveLogReadersMu.Lock()
			defer s.liveLogReadersMu.Unlock()
			s.liveLogReaders[key] = logReader
		},
		OnWorkspaceTeardown: func() {
			s.liveArchiversMu.Lock()
			delete(s.liveArchivers, key)
			s.liveArchiversMu.Unlock()

			s.liveLogReadersMu.Lock()
			delete(s.liveLogReaders, key)
			s.liveLogReadersMu.Unlock()
		},
	}

	task.Log.Info("Running task")
	s.broadcast(EventTaskRunning{Job: task.Job.FQN(), Task: task.Name})

	// This blocks for the entire task duration (minutes to hours)
	if exitCode, err := node.RunTask(ctx, task, runConfig); err != nil {
		if ctx.Err() != nil {
			task.Log.Info("Task aborted")
			s.broadcast(EventTaskAborted{Job: task.Job.FQN(), Task: task.Name})
		} else if exitCode == 42 {
			task.Log.Info("Task ran successfully, but reported issues", "error", err)
			s.broadcast(EventTaskFailed{Job: task.Job.FQN(), Task: task.Name, ExitCode: exitCode})
		} else {
			task.Log.Warn("Task failed while running", "error", err)
			s.broadcast(EventTaskFailed{Job: task.Job.FQN(), Task: task.Name, ExitCode: exitCode})
		}
	} else {
		task.Log.Info("Task completed")
		s.broadcast(EventTaskCompleted{Job: task.Job.FQN(), Task: task.Name})
	}

	// Atomic counter: multiple watchTaskExecution goroutines may finish concurrently.
	// When the last task of a job completes, broadcast EventJobCompleted.
	if task.Job.tasksCompleted.Add(1) == uint32(len(task.Job.Tasks)) {
		s.log.Info("Job completed", "name", task.Job.FQN())
		s.broadcast(EventJobCompleted{Job: task.Job.FQN()})
	}

	// Free all node slots occupied by this task on the main goroutine (via deferred channel)
	// to avoid a data race with scheduleTaskOnOnlineNode() which reads nodeState.tasks
	// from the main loop. wg.Done() is unconditional and outside the select so Wait() never hangs.
	select {
	case s.deferred <- func() {
		freed := 0
		for slot, t := range nodeState.tasks {
			if t == task {
				s.broadcast(EventNodeSlotUpdated{Node: nodeState.nodeName, Slot: slot, Task: nil})
				nodeState.tasks[slot] = nil
				freed++
			}
		}
		task.Log.Debug("Freed task slots", "node", nodeState.nodeName, "slotsFreed", freed)
		s.requestTick("executed task should give its slots to another task")
	}:
	case <-s.stop:
	}
	s.wg.Done() // matches wg.Add in Schedule()
}

// watchNodeTermination runs in its own goroutine (spawned from resizePool when a node
// has all empty slots). It blocks on node.Terminate() which cleans up the node (no-op
// for local Docker, VM deletion for OpenStack).
//
// The s.nodes mutation is sent to the main goroutine via the deferred channel to avoid
// a data race with resizePool() which iterates s.nodes on the main loop.
func (s *Scheduler) watchNodeTermination(nodeState *nodeState) {
	nodeState.log.Info("Terminating node")
	// Note: UpdateStatus(NodeStatusTerminating) is called by resizePool on the main goroutine
	// before spawning this goroutine, to avoid a data race on nodeState.status.

	err := nodeState.node.Terminate()

	select {
	case s.deferred <- func() {
		if err != nil {
			// TODO: retry
			nodeState.log.Error("Termination of node failed", "error", err)
			nodeState.UpdateStatus(NodeStatusFailedTerminating)
		} else {
			nodeState.log.Info("Node terminated")
			nodeState.UpdateStatus(NodeStatusTerminated)
		}
		s.nodes = lo.Without(s.nodes, nodeState)
		s.broadcast(EventNodeTerminated{Node: nodeState.nodeName})
		s.requestTick("terminated node should give its slot to another node")
	}:
	case <-s.stop:
	}
}

func (s *Scheduler) removeQueuedNodes(count int) {
	removed := 0
	for i := len(s.nodesQueue) - 1; i >= 0 && removed < count; i-- {
		ns := s.nodesQueue[i]
		s.broadcast(EventNodeTerminated{Node: ns.nodeName})
		s.nodesQueue = append(s.nodesQueue[:i], s.nodesQueue[i+1:]...)
		removed++
	}
}

func (s *Scheduler) emptyNodesQueue() {
	for _, ns := range s.nodesQueue {
		s.broadcast(EventNodeTerminated{Node: ns.nodeName})
	}
	s.nodesQueue = nil
}

