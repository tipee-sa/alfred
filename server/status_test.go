package main

import (
	"testing"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slotTasks returns the task name in each slot of the node, "" for an empty slot.
func slotTasks(node *proto.NodeStatus) []string {
	names := make([]string, len(node.Slots))
	for i, slot := range node.Slots {
		if slot != nil && slot.Task != nil {
			names[i] = slot.Task.Name
		}
	}
	return names
}

// applyEvents runs listenEvents over the given events and returns once it has applied them all.
func applyEvents(events ...schedulerpkg.Event) {
	c := make(chan schedulerpkg.Event, len(events))
	for _, event := range events {
		c <- event
	}
	close(c)
	listenEvents(c)
}

func TestListenEvents_MultiSlotUpdate(t *testing.T) {
	serverStatusMutex.Lock()
	serverStatus.Nodes = nil
	serverStatusMutex.Unlock()
	defer func() { serverStatus.Nodes = nil }()

	applyEvents(schedulerpkg.EventNodeCreated{Node: "node", Status: schedulerpkg.NodeStatusOnline, NumSlots: 4})
	require.Len(t, serverStatus.Nodes, 1)
	node := serverStatus.Nodes[0]

	// One event places a multi-slot task on all its slots at once
	applyEvents(schedulerpkg.EventNodeSlotUpdated{Node: "node", Slots: []int{0, 2, 3}, Task: &schedulerpkg.NodeSlotTask{Job: "job", Name: "big"}})
	assert.Equal(t, []string{"big", "", "big", "big"}, slotTasks(node))

	// Out-of-range slots are skipped, the valid ones still applied
	applyEvents(schedulerpkg.EventNodeSlotUpdated{Node: "node", Slots: []int{1, 7}, Task: &schedulerpkg.NodeSlotTask{Job: "job", Name: "small"}})
	assert.Equal(t, []string{"big", "small", "big", "big"}, slotTasks(node))

	// And one event frees them all
	applyEvents(schedulerpkg.EventNodeSlotUpdated{Node: "node", Slots: []int{0, 2, 3}, Task: nil})
	assert.Equal(t, []string{"", "small", "", ""}, slotTasks(node))
}
