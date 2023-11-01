package scheduler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/gammadia/alfred/namegen"
	"github.com/samber/lo"
)

type Scheduler struct {
	provisioner Provisioner
	config      Config
	log         *slog.Logger

	input        chan *Task
	tickRequests chan any
	deferred     chan func()

	queue []*Task

	nodes                 []*nodeState
	provisionedNodes      int
	lastNodeProvisionedAt time.Time

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

		input:        make(chan *Task),
		tickRequests: make(chan any, 1),
		deferred:     make(chan func()),

		queue: nil,

		events:    make(chan Event, 64),
		listeners: make(map[chan Event]bool),

		stop: make(chan any),
		wg:   sync.WaitGroup{},
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

func (s *Scheduler) Schedule(job *Job) string {
	job.id = namegen.Get()
	s.log.Info("Scheduling job", "name", job.FQN())

	s.broadcast(EventJobScheduled{Job: job.FQN(), About: job.About, Tasks: job.Tasks})
	s.wg.Add(len(job.Tasks))
	for _, name := range job.Tasks {
		task := Task{
			Job:  job,
			Name: name,

			log: s.log.With(slog.Group("task", "job", job.FQN(), "name", name)),
		}

		s.input <- &task
	}

	return job.FQN()
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
		case task := <-s.input:
			if s.shutdown {
				task.log.Debug("Scheduler is shutting down, ignoring task")
			}
			s.broadcast(EventTaskQueued{Job: task.Job.FQN(), Task: task.Name})
			s.queue = append(s.queue, task)
			s.requestTick()

		case <-s.tickRequests:
			// Attempt to schedule as many tasks as possible
			for len(s.queue) > 0 {
				if !s.scheduleTaskOnNode() {
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
func (s *Scheduler) requestTick() {
	select {
	case s.tickRequests <- nil: // Yeah! 🎉
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

func (s *Scheduler) scheduleTaskOnNode() bool {
	nextTask := s.queue[0]

	for _, nodeState := range s.nodes {
		if nodeState.status != NodeStatusOnline {
			continue
		}

		for slot, runningTask := range nodeState.tasks {
			if runningTask == nil {
				nextTask.log.Info("Scheduling task on node", "slot", fmt.Sprintf("%s:%d", nodeState.node.Name(), slot))
				nodeState.tasks[slot] = nextTask
				s.queue = s.queue[1:]
				s.broadcast(EventNodeSlotUpdated{Node: nodeState.nodeName, Slot: slot, Task: &NodeSlotTask{nextTask.Job.Name, nextTask.Name}})

				go s.watchTaskExecution(nodeState, slot, nextTask)
				return true
			}
		}
	}

	return false
}

func (s *Scheduler) resizePool() {
	pendingNodes := 0

	for _, nodeState := range s.nodes {
		switch nodeState.status {
		case NodeStatusProvisioning:
			pendingNodes += 1

		case NodeStatusOnline:
			if lo.EveryBy(nodeState.tasks, func(task *Task) bool {
				return task == nil
			}) {
				nodeState.log.Info("Terminating node")
				nodeState.UpdateStatus(NodeStatusTerminating)

				go s.watchNodeTermination(nodeState)
			}
		}
	}

	if len(s.queue) < 1 || len(s.nodes) >= s.config.MaxNodes {
		return
	}

	// At this point, we need more nodes!

	incomingCapacity := pendingNodes * s.config.TasksPerNode
	requiredNodes := math.Ceil(float64(len(s.queue)-incomingCapacity) / float64(s.config.TasksPerNode))
	maximumMoreNodes := float64(s.config.MaxNodes - len(s.nodes))

	nodesToProvision := int(math.Min(requiredNodes, maximumMoreNodes))

	var delay time.Duration
	for i := 0; i < nodesToProvision; i++ {
		if s.provisionedNodes > 0 {
			delay = s.config.ProvisioningDelay
		}

		s.provisionedNodes += 1
		s.lastNodeProvisionedAt = lo.Must(lo.Coalesce(s.lastNodeProvisionedAt, time.Now())).Add(delay)

		nodeName := namegen.Get()
		nodeState := &nodeState{
			scheduler: s,

			node:   nil,
			status: NodeStatusPending,
			tasks:  make([]*Task, s.config.TasksPerNode),
			log:    s.log.With("component", "node").With(slog.Group("node", "name", nodeName)),

			nodeName:      nodeName,
			earliestStart: s.lastNodeProvisionedAt,
		}
		s.nodes = append(s.nodes, nodeState)
		s.broadcast(EventNodeCreated{Node: nodeName, Status: nodeState.status})

		go s.watchNodeProvisioning(nodeState)
	}
}

func (s *Scheduler) watchNodeProvisioning(nodeState *nodeState) {
	now := time.Now()
	if nodeState.status == NodeStatusPending && now.Before(nodeState.earliestStart) {
		wait := nodeState.earliestStart.Sub(now)
		nodeState.log.Info("Waiting before provisioning node", "wait", wait)
		time.Sleep(wait)
	}

	nodeState.log.Info("Provisioning node")

	nodeState.UpdateStatus(NodeStatusProvisioning)
	if node, err := s.provisioner.Provision(nodeState.nodeName); err != nil {
		nodeState.log.Error("Provisioning of node failed", "error", err)
		nodeState.UpdateStatus(NodeStatusFailed)

		s.after(s.config.ProvisioningFailureCooldown, func() {
			s.nodes = lo.Without(s.nodes, nodeState)
			s.requestTick()
		})
	} else {
		nodeState.node = node
		nodeState.UpdateStatus(NodeStatusOnline)
		nodeState.log.Info("Node is online")
	}

	s.requestTick()
}

func (s *Scheduler) watchTaskExecution(nodeState *nodeState, slot int, task *Task) {
	node := nodeState.node

	runConfig := RunTaskConfig{
		ArtifactPreserver: s.config.ArtifactPreserver,
	}

	s.broadcast(EventTaskRunning{Job: task.Job.FQN(), Task: task.Name})
	if exitCode, err := node.RunTask(task, runConfig); err != nil {
		s.broadcast(EventTaskFailed{Job: task.Job.FQN(), Task: task.Name, ExitCode: exitCode})
		task.log.Warn("Task failed", "error", err)
	} else {
		s.broadcast(EventTaskCompleted{Job: task.Job.FQN(), Task: task.Name})
		task.log.Info("Task completed")
	}

	if task.Job.tasksCompleted.Add(1) == uint32(len(task.Job.Tasks)) {
		s.broadcast(EventJobCompleted{Job: task.Job.FQN()})
	}

	s.broadcast(EventNodeSlotUpdated{Node: nodeState.nodeName, Slot: slot, Task: nil})
	nodeState.tasks[slot] = nil

	s.wg.Done()
	s.requestTick()
}

func (s *Scheduler) watchNodeTermination(nodeState *nodeState) {
	node := nodeState.node

	if err := node.Terminate(); err != nil {
		// TODO: retry
		nodeState.log.Error("Termination of node failed", "error", err)
	} else {
		nodeState.log.Info("Node terminated")
	}

	s.nodes = lo.Without(s.nodes, nodeState)
	s.broadcast(EventNodeTerminated{Node: nodeState.nodeName})
	s.requestTick()
}
