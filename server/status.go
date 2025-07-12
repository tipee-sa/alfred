package main

import (
	"sync"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"github.com/gammadia/alfred/server/log"
	"github.com/samber/lo"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var serverStatus *proto.Status
var serverStatusMutex sync.RWMutex

var clientListeners = map[chan schedulerpkg.Event]ClientListenerFilter{}
var clientListenersMutex sync.RWMutex

type ClientListenerFilter func(schedulerpkg.Event) bool

func init() {
	serverStatus = &proto.Status{}
	serverStatus.Server = &proto.Status_Server{}
	serverStatus.Scheduler = &proto.Status_Scheduler{}
}

func listenEvents(c <-chan schedulerpkg.Event) {
	//eventLogger := log.Base.With("component", "events")
	for event := range c {
		// TODO: fix serialization of payload
		//eventLogger.Info(reflect.TypeOf(event).Name(), "payload", event)

		serverStatusMutex.Lock()

		switch event := event.(type) {
		// Node events
		case schedulerpkg.EventNodeCreated:
			serverStatus.Nodes = append(serverStatus.Nodes, &proto.NodeStatus{
				Name:   event.Node,
				Status: event.Status.AsProto(),
				Slots:  make([]*proto.NodeStatus_Slot, serverStatus.Scheduler.TasksPerNodes),
			})
		case schedulerpkg.EventNodeStatusUpdated:
			for _, node := range serverStatus.Nodes {
				if node.Name == event.Node {
					node.Status = event.Status.AsProto()
					break
				}
			}
		case schedulerpkg.EventNodeSlotUpdated:
			for _, node := range serverStatus.Nodes {
				if node.Name == event.Node {
					var task *proto.NodeStatus_Slot_Task
					if event.Task != nil {
						task = &proto.NodeStatus_Slot_Task{
							Job:  event.Task.Job,
							Name: event.Task.Name,
						}
					}
					node.Slots[event.Slot] = &proto.NodeStatus_Slot{
						Id:   uint32(event.Slot),
						Task: task,
					}
					break
				}
			}
		case schedulerpkg.EventNodeTerminated:
			for i, node := range serverStatus.Nodes {
				if node.Name == event.Node {
					serverStatus.Nodes = append(serverStatus.Nodes[:i], serverStatus.Nodes[i+1:]...)
					break
				}
			}

		// Job events
		case schedulerpkg.EventJobScheduled:
			serverStatus.Jobs = append(serverStatus.Jobs, &proto.JobStatus{
				Name:        event.Job,
				About:       event.About,
				Tasks:       make([]*proto.TaskStatus, 0, len(event.Tasks)),
				ScheduledAt: timestamppb.Now(),
			})
		case schedulerpkg.EventJobCompleted:
			for _, job := range serverStatus.Jobs {
				if job.Name == event.Job {
					job.CompletedAt = timestamppb.Now()
					break
				}
			}

		// Tasks events
		case schedulerpkg.EventTaskQueued:
			for _, job := range serverStatus.Jobs {
				if job.Name == event.Job {
					job.Tasks = append(job.Tasks, &proto.TaskStatus{
						Name:   event.Task,
						Status: proto.TaskStatus_QUEUED,
					})
					break
				}
			}
		case schedulerpkg.EventTaskRunning:
			for _, job := range serverStatus.Jobs {
				if job.Name == event.Job {
					for _, task := range job.Tasks {
						if task.Name == event.Task {
							task.Status = proto.TaskStatus_RUNNING
							task.StartedAt = timestamppb.Now()
							break
						}
					}
					break
				}
			}
		case schedulerpkg.EventTaskAborted:
			for _, job := range serverStatus.Jobs {
				if job.Name == event.Job {
					for _, task := range job.Tasks {
						if task.Name == event.Task {
							task.Status = proto.TaskStatus_ABORTED
							task.EndedAt = timestamppb.Now()
							break
						}
					}
					break
				}
			}
		case schedulerpkg.EventTaskFailed:
			for _, job := range serverStatus.Jobs {
				if job.Name == event.Job {
					for _, task := range job.Tasks {
						if task.Name == event.Task {
							task.Status = proto.TaskStatus_FAILED
							task.ExitCode = lo.ToPtr(int32(event.ExitCode))
							task.EndedAt = timestamppb.Now()
							break
						}
					}
					break
				}
			}
		case schedulerpkg.EventTaskCompleted:
			for _, job := range serverStatus.Jobs {
				if job.Name == event.Job {
					for _, task := range job.Tasks {
						if task.Name == event.Task {
							task.Status = proto.TaskStatus_COMPLETED
							task.ExitCode = lo.ToPtr(int32(0))
							task.EndedAt = timestamppb.Now()
							break
						}
					}
					break
				}
			}
		}

		serverStatusMutex.Unlock()

		clientListenersMutex.RLock()
		for channel, filter := range clientListeners {
			if filter != nil && !filter(event) {
				continue
			}
			channel <- event
		}
		clientListenersMutex.RUnlock()
	}
}

func addClientListener(filter ClientListenerFilter) (events chan schedulerpkg.Event, cancel func()) {
	clientListenersMutex.Lock()
	defer clientListenersMutex.Unlock()

	log.Debug("Added client listener")
	channel := make(chan schedulerpkg.Event, 1024)
	clientListeners[channel] = filter

	return channel, func() {
		clientListenersMutex.Lock()
		defer clientListenersMutex.Unlock()

		log.Debug("Removed client listener")
		delete(clientListeners, channel)
	}
}
