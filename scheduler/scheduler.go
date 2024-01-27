package scheduler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gammadia/alfred/namegen"
	"github.com/gammadia/alfred/scheduler/internal"
	"github.com/samber/lo"
)

type Scheduler struct {
	provisioner Provisioner
	config      Config
	log         *slog.Logger

	input        chan *Job
	tickRequests chan any
	deferred     chan func()

	tasksQueue []*Task
	nodes      []*nodeState

	nodesQueue            []*nodeState
	earliestNextNodeStart time.Time

	events         chan Event
	listeners      map[chan Event]bool
	listenersMutex sync.RWMutex

	shutdown bool
	stop     chan any

	// WaitGroup tracking all running tasks
	wg sync.WaitGroup
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
	}

	scheduler.log.Debug("Scheduler config", "config", string(lo.Must(json.Marshal(config))))
	go scheduler.forwardEvents()

	return scheduler
}

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
				// Event sent
			default:
				s.log.Debug("Listener queue full, dropping message")
			}
		}
	}
	for event := range s.events {
		emit(event)
	}
}

func (s *Scheduler) Schedule(job *Job) (string, error) {
	job.id = namegen.Get()
	nbTasks := len(job.Tasks)

	if s.shutdown {
		return job.FQN(), fmt.Errorf("scheduler is shutting down, ignoring job '%s' (%d tasks)", job.Name, nbTasks)
	}

	s.log.Info("Scheduling job", "name", job.FQN(), "tasks", nbTasks)
	s.broadcast(EventJobScheduled{Job: job.FQN(), About: job.About, Tasks: job.Tasks})

	s.wg.Add(nbTasks)
	s.input <- job

	return job.FQN(), nil
}

func (s *Scheduler) Wait() {
	s.wg.Wait()
	s.provisioner.Wait()
}

func (s *Scheduler) Shutdown() {
	close(s.stop)
	go func() {
		s.Wait()
		close(s.events)
	}()
}

func (s *Scheduler) Run() {
	s.log.Info("Scheduler is running")

	for {
		select {
		case job := <-s.input:
			for _, name := range job.Tasks {
				task := &Task{
					Job:  job,
					Name: name,
					Log:  s.log.With(slog.Group("task", "job", job.FQN(), "name", name)),
				}

				task.Log.Debug("Queuing task")
				s.broadcast(EventTaskQueued{Job: task.Job.FQN(), Task: task.Name})
				s.tasksQueue = append(s.tasksQueue, task)
			}
			s.requestTick("job tasks should be scheduled")

		case <-s.tickRequests:
			// Attempt to schedule as many tasks as possible
			for len(s.tasksQueue) > 0 {
				if !s.scheduleTaskOnOnlineNode() {
					break
				}
			}
			s.resizePool()

		case f := <-s.deferred:
			f()

		case <-s.stop:
			s.log.Info("Shutting down scheduler")
			s.shutdown = true
			s.provisioner.Shutdown()

			s.log.Info("Terminating online nodes")
			for _, nodeState := range s.nodes {
				// TODO: canceling of non running nodes
				if nodeState.status == NodeStatusOnline {
					go nodeState.node.Terminate() // TODO (maybe): handle error
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

// after schedules a function to be executed after a delay
// This function is safe to call from multiple goroutines
// The function is run on the main scheduler goroutine
func (s *Scheduler) after(d time.Duration, f func()) {
	// TODO: what to do with that if the scheduler is shut down?
	time.AfterFunc(d, func() {
		s.deferred <- f
	})
}

func (s *Scheduler) scheduleTaskOnOnlineNode() bool {
	nextTask := s.tasksQueue[0]

	for _, nodeState := range s.nodes {
		if nodeState.status != NodeStatusOnline {
			continue
		}

		for slot, runningTask := range nodeState.tasks {
			if runningTask == nil {
				nodeState.tasks[slot] = nextTask
				s.tasksQueue = s.tasksQueue[1:]

				nextTask.Log.Info("Scheduling task on node", "slot", fmt.Sprintf("%s:%d", nodeState.node.Name(), slot), "remainingTasks", len(s.tasksQueue))
				s.broadcast(EventNodeSlotUpdated{Node: nodeState.nodeName, Slot: slot, Task: &NodeSlotTask{nextTask.Job.Name, nextTask.Name}})

				go s.watchTaskExecution(nodeState, slot, nextTask)
				return true
			}
		}
	}

	return false
}

func (s *Scheduler) resizePool() {
	// Remove discarded nodes
	s.nodes = lo.Filter(s.nodes, func(nodeState *nodeState, _ int) bool {
		return nodeState.status != NodeStatusDiscarded
	})

	// Terminate nodes that have no more tasks running on them
	for _, nodeState := range s.nodes {
		if nodeState.status == NodeStatusOnline && lo.EveryBy(nodeState.tasks, func(task *Task) bool {
			return task == nil
		}) {
			go s.watchNodeTermination(nodeState)
		}
	}

	// Make sure we need more nodes
	if len(s.tasksQueue) < 1 || len(s.nodes) >= s.config.MaxNodes {
		s.emptyNodesQueue()
		return
	}
	s.log.Debug("Need more nodes",
		"tasksQueue", len(s.tasksQueue),
		"maxNodes", s.config.MaxNodes,
		"maxTasksPerNode", s.config.TasksPerNode,
		"nodes", len(s.nodes),
	)
	nodesToCreate := 0

	// First, we check if provisioning nodes cover our needs
	provisioningNodes := lo.Reduce(s.nodes, func(acc int, nodeState *nodeState, _ int) int {
		return lo.Ternary(nodeState.status == NodeStatusProvisioning, acc+1, acc)
	}, 0)
	if nodesToCreate = s.nbNodesToCreate(provisioningNodes); nodesToCreate < 1 {
		s.log.Debug("Provisioning nodes cover our needs", "needed", nodesToCreate, "provisioningNodes", provisioningNodes)
		s.emptyNodesQueue()
		// Here we don't need to remove extra provisioning machines, as they will be discarded as soon as they are online
		return
	}
	s.log.Debug("Provisioning nodes are not enough", "needed", nodesToCreate, "provisioningNodes", provisioningNodes)

	// Then, we check if nodes in the queue that we can now provision cover our needs
	queuedNodes := 0
	provisioningQueueNodes := 0
	for _, nodeState := range s.nodesQueue {
		if time.Now().After(nodeState.earliestStart) {
			nodeState.log.Info("Provisioning node from the queue")
			nodeState.UpdateStatus(NodeStatusProvisioning)

			s.nodesQueue = lo.Without(s.nodesQueue, nodeState)
			s.nodes = append(s.nodes, nodeState)
			go s.watchNodeProvisioning(nodeState)

			provisioningQueueNodes += 1

			if nodesToCreate = s.nbNodesToCreate(provisioningNodes + provisioningQueueNodes); nodesToCreate < 1 {
				s.log.Debug("Provisioning nodes from the queue cover our needs",
					"needed", nodesToCreate,
					"provisioningNodes", provisioningNodes,
					"provisioningQueueNodes", provisioningQueueNodes,
				)
				s.emptyNodesQueue()
				return
			} else {
				s.log.Debug("Provisioning nodes from the queue are not enough",
					"needed", nodesToCreate,
					"provisioningNodes", provisioningNodes,
					"provisioningQueueNodes", provisioningQueueNodes,
				)
			}
		} else {
			queuedNodes += 1
		}
	}

	// Finally, we check if nodes queued in the future cover our needs
	if nodesToCreate = s.nbNodesToCreate(provisioningNodes + provisioningQueueNodes + queuedNodes); nodesToCreate < 1 {
		s.log.Debug("Provisioning nodes and queued nodes cover our needs",
			"needed", nodesToCreate,
			"provisioningNodes", provisioningNodes,
			"provisioningQueueNodes", provisioningQueueNodes,
			"queuedNodes", queuedNodes,
		)
		if nodesToCreate < 0 {
			s.log.Debug("Remove extra nodes from the queue", "nodesToRemove", -nodesToCreate)
			s.nodesQueue = s.nodesQueue[:len(s.nodesQueue)+nodesToCreate]
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
		// Never queue the first node we create
		queueNode := (len(s.nodes) > 0 || len(s.nodesQueue) > 0 || i > 0) && time.Now().Before(s.earliestNextNodeStart)

		nodeName := namegen.Get()
		ns := &nodeState{
			scheduler: s,

			node:   nil,
			status: lo.Ternary(queueNode, NodeStatusQueued, NodeStatusProvisioning),
			tasks:  make([]*Task, s.config.TasksPerNode),
			log:    s.log.With("component", "node").With(slog.Group("node", "name", nodeName)),

			nodeName:      nodeName,
			earliestStart: s.earliestNextNodeStart,
		}

		ns.log.Debug("Creating node", "earliestStart", ns.earliestStart, "status", ns.status)
		s.broadcast(EventNodeCreated{Node: nodeName, Status: ns.status})

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

func (s *Scheduler) watchNodeProvisioning(nodeState *nodeState) {
	nodeState.log.Info("Provisioning node")
	nodeState.UpdateStatus(NodeStatusProvisioning)

	if node, err := s.provisioner.Provision(nodeState.nodeName); err != nil {
		nodeState.log.Error("Provisioning of node failed", "error", err)
		nodeState.UpdateStatus(NodeStatusFailedProvisioning)

		s.after(s.config.ProvisioningFailureCooldown, func() {
			nodeState.log.Info("Discarding failed node")
			nodeState.UpdateStatus(NodeStatusDiscarded)
			s.requestTick("discarded node (because of failed provisioning) should be removed")
		})
	} else {
		nodeState.node = node
		nodeState.log.Info("Node is online")
		nodeState.UpdateStatus(NodeStatusOnline)
		s.requestTick("online node should be ready for duty")
	}
}

func (s *Scheduler) watchTaskExecution(nodeState *nodeState, slot int, task *Task) {
	node := nodeState.node

	runConfig := RunTaskConfig{
		ArtifactPreserver: s.config.ArtifactPreserver,
		SecretLoader:      s.config.SecretLoader,
	}

	task.Log.Info("Running task")
	s.broadcast(EventTaskRunning{Job: task.Job.FQN(), Task: task.Name})

	if exitCode, err := node.RunTask(task, runConfig); err != nil {
		if exitCode == 42 {
			task.Log.Info("Task ran successfully, but reported issues", "error", err)
		} else {
			task.Log.Warn("Task failed while running", "error", err)
		}
		s.broadcast(EventTaskFailed{Job: task.Job.FQN(), Task: task.Name, ExitCode: exitCode})
	} else {
		task.Log.Info("Task completed")
		s.broadcast(EventTaskCompleted{Job: task.Job.FQN(), Task: task.Name})
	}

	if task.Job.tasksCompleted.Add(1) == uint32(len(task.Job.Tasks)) {
		s.log.Info("Job completed", "name", task.Job.FQN())
		s.broadcast(EventJobCompleted{Job: task.Job.FQN()})
	}

	s.broadcast(EventNodeSlotUpdated{Node: nodeState.nodeName, Slot: slot, Task: nil})
	nodeState.tasks[slot] = nil

	s.wg.Done()
	s.requestTick("executed task should give its slot to another task")
}

func (s *Scheduler) watchNodeTermination(nodeState *nodeState) {
	nodeState.log.Info("Terminating node")
	nodeState.UpdateStatus(NodeStatusTerminating)

	if err := nodeState.node.Terminate(); err != nil {
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
}

func (s *Scheduler) emptyNodesQueue() {
	if len(s.nodesQueue) < 1 {
		return
	}
	s.log.Debug("Emptying nodes queue", "nodes", len(s.nodesQueue))
	s.nodesQueue = nil
}

func (s *Scheduler) nbNodesToCreate(incomingNodes int) int {
	return internal.NbNodesToCreate(s.config.MaxNodes, s.config.TasksPerNode, len(s.tasksQueue), len(s.nodes), incomingNodes)
}
