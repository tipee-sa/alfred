package scheduler

type NodeSlotTask struct {
	Job  string
	Name string
}

type Event interface{}

// Nodes

type EventNodeCreated struct {
	Node     string
	Status   NodeStatus
	NumSlots int
}

type EventNodeStatusUpdated struct {
	Node   string
	Status NodeStatus
}

type EventNodeSlotUpdated struct {
	Node string
	Slot int
	Task *NodeSlotTask
}

type EventNodeTerminated struct {
	Node string
}

// Jobs

type EventJobScheduled struct {
	Job         string
	About       string
	Tasks       []string
	Jobfile     string
	CommandLine string
	StartedBy   string
}

type EventJobCompleted struct {
	Job string
}

// Tasks

type EventTaskQueued struct {
	Job  string
	Task string
}

type EventTaskRunning struct {
	Job  string
	Task string
}

type EventTaskAborted struct {
	Job  string
	Task string
}

type EventTaskFailed struct {
	Job      string
	Task     string
	ExitCode int
}

type EventTaskSkipped struct {
	Job      string
	Task     string
	ExitCode int
}

type EventTaskTimedOut struct {
	Job  string
	Task string
}

type EventTaskCompleted struct {
	Job  string
	Task string
}
