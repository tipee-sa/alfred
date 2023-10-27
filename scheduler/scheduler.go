package scheduler

import (
	"log"
	"math"
	"sync"
	"time"

	"github.com/gammadia/alfred/namegen"
	"github.com/samber/lo"
)

type Scheduler struct {
	name        namegen.ID
	provisioner Provisioner
	config      Config
	logger      *log.Logger
	shutdown    bool

	jobs map[namegen.ID]*Job

	input        chan *Task
	tickRequests chan any
	deferred     chan func()

	queue []*Task

	nodes []*nodeState

	stop chan any
	wg   sync.WaitGroup
}

func NewScheduler(provisioner Provisioner, config Config) *Scheduler {
	scheduler := &Scheduler{
		name:        namegen.Get(),
		provisioner: provisioner,
		config:      config,
		logger:      log.Default(),

		jobs: make(map[namegen.ID]*Job),

		input:        make(chan *Task),
		tickRequests: make(chan any, 1),
		deferred:     make(chan func()),

		queue: nil,

		stop: make(chan any),
		wg:   sync.WaitGroup{},
	}

	provisioner.SetLogger(scheduler.logger)

	go scheduler.Run()
	return scheduler
}

func (s *Scheduler) Schedule(job *Job) {
	job.Name = namegen.Get()
	s.logger.Printf("Starting job '%s'", job.Name)

	s.wg.Add(len(job.Tasks))
	for _, name := range job.Tasks {
		task := Task{
			Job:    job,
			Name:   name,
			Status: TaskStatusPending,
		}

		s.input <- &task
	}
}

func (s *Scheduler) Wait() {
	s.wg.Wait()
	s.provisioner.Wait()
}

func (s *Scheduler) Shutdown() {
	close(s.stop)
}

func (s *Scheduler) Run() {
	s.logger.Printf("Scheduler is running")

	for {
		select {
		case task := <-s.input:
			if s.shutdown {
				s.logger.Printf("Scheduler is shutting down, ignoring task '%s'", task.Name)
			}
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
			s.logger.Printf("Scheduler is stopping")
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
				s.logger.Printf("Scheduling task '%s' on node '%s:%d'", nextTask.Name, nodeState.node.Name(), slot)
				nodeState.tasks[slot] = nextTask
				s.queue = s.queue[1:]

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
				s.logger.Printf("Terminating node '%s'", nodeState.node.Name())
				nodeState.status = NodeStatusTerminating

				go s.watchNodeTermination(nodeState)
			}
		}
	}

	if len(s.queue) < 1 || len(s.nodes) >= s.provisioner.MaxNodes() {
		return
	}

	// At this point, we need more nodes!

	incomingCapacity := pendingNodes * s.provisioner.MaxTasksPerNode()
	requiredNodes := math.Ceil(float64(len(s.queue)-incomingCapacity) / float64(s.provisioner.MaxTasksPerNode()))
	maximumMoreNodes := float64(s.provisioner.MaxNodes() - len(s.nodes))

	nodesToProvision := int(math.Min(requiredNodes, maximumMoreNodes))

	for i := 0; i < nodesToProvision; i++ {
		nodeState := &nodeState{
			node:   nil,
			status: NodeStatusProvisioning,
			tasks:  make([]*Task, s.provisioner.MaxTasksPerNode()),
		}
		s.nodes = append(s.nodes, nodeState)
		s.logger.Printf("Provisioning a new node")

		go s.watchNodeProvisioning(nodeState)
	}
}

func (s *Scheduler) watchNodeProvisioning(nodeState *nodeState) {
	if node, err := s.provisioner.Provision(); err != nil {
		s.logger.Printf("Provisioning of node failed: %s", err)
		nodeState.status = NodeStatusFailed

		s.after(s.config.ProvisioningFailureCooldown, func() {
			s.nodes = lo.Without(s.nodes, nodeState)
			s.requestTick()
		})
	} else {
		s.logger.Printf("Node '%s' is online", node.Name())
		nodeState.node = node
		nodeState.status = NodeStatusOnline
	}

	s.requestTick()
}

func (s *Scheduler) watchTaskExecution(nodeState *nodeState, slot int, task *Task) {
	node := nodeState.node

	if err := node.Run(task); err != nil {
		s.logger.Printf("Task '%s' failed: %s", task.Name, err)
		task.Status = TaskStatusFailed
	} else {
		s.logger.Printf("Task '%s' completed", task.Name)
		task.Status = TaskStatusCompleted
	}

	nodeState.tasks[slot] = nil
	s.wg.Done()

	s.requestTick()
}

func (s *Scheduler) watchNodeTermination(nodeState *nodeState) {
	node := nodeState.node

	if err := node.Terminate(); err != nil {
		// TODO: retry
		s.logger.Printf("Termination of node '%s' failed: %s", node.Name(), err)
	} else {
		s.logger.Printf("Node '%s' terminated", node.Name())
	}

	s.nodes = lo.Without(s.nodes, nodeState)
	s.requestTick()
}
