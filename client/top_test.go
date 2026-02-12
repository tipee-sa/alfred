package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

// --- nodeStatusColor ---

func TestNodeStatusColor_Online(t *testing.T) {
	assert.Equal(t, tcell.ColorGreen, nodeStatusColor(proto.NodeStatus_ONLINE))
}

func TestNodeStatusColor_Provisioning(t *testing.T) {
	assert.Equal(t, tcell.ColorYellow, nodeStatusColor(proto.NodeStatus_QUEUED))
	assert.Equal(t, tcell.ColorYellow, nodeStatusColor(proto.NodeStatus_PROVISIONING))
}

func TestNodeStatusColor_Terminating(t *testing.T) {
	assert.Equal(t, tcell.ColorGray, nodeStatusColor(proto.NodeStatus_TERMINATING))
	assert.Equal(t, tcell.ColorGray, nodeStatusColor(proto.NodeStatus_TERMINATED))
}

func TestNodeStatusColor_Failed(t *testing.T) {
	assert.Equal(t, tcell.ColorRed, nodeStatusColor(proto.NodeStatus_FAILED_PROVISIONING))
	assert.Equal(t, tcell.ColorRed, nodeStatusColor(proto.NodeStatus_FAILED_TERMINATING))
	assert.Equal(t, tcell.ColorRed, nodeStatusColor(proto.NodeStatus_DISCARDED))
}

func TestNodeStatusColor_Unknown(t *testing.T) {
	assert.Equal(t, tcell.ColorWhite, nodeStatusColor(proto.NodeStatus_UNKNOWN))
}

// --- formatDuration ---

func TestFormatDuration_Seconds(t *testing.T) {
	assert.Equal(t, "0s", formatDuration(0))
	assert.Equal(t, "5s", formatDuration(5*time.Second))
	assert.Equal(t, "59s", formatDuration(59*time.Second))
}

func TestFormatDuration_Minutes(t *testing.T) {
	assert.Equal(t, "1m 00s", formatDuration(1*time.Minute))
	assert.Equal(t, "5m 30s", formatDuration(5*time.Minute+30*time.Second))
	assert.Equal(t, "59m 59s", formatDuration(59*time.Minute+59*time.Second))
}

func TestFormatDuration_Hours(t *testing.T) {
	assert.Equal(t, "1h 00m 00s", formatDuration(1*time.Hour))
	assert.Equal(t, "2h 15m 03s", formatDuration(2*time.Hour+15*time.Minute+3*time.Second))
}

func TestFormatDuration_TruncatesMilliseconds(t *testing.T) {
	assert.Equal(t, "5s", formatDuration(5*time.Second+500*time.Millisecond))
}

// --- taskProgress ---

func TestTaskProgress_Empty(t *testing.T) {
	assert.Equal(t, "", taskProgress(nil))
	assert.Equal(t, "", taskProgress([]*proto.TaskStatus{}))
}

func TestTaskProgress_AllQueued(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Name: "t1", Status: proto.TaskStatus_QUEUED},
		{Name: "t2", Status: proto.TaskStatus_QUEUED},
	}
	assert.Equal(t, "[white]2 queue[-]", taskProgress(tasks))
}

func TestTaskProgress_AllCompleted(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Name: "t1", Status: proto.TaskStatus_COMPLETED},
		{Name: "t2", Status: proto.TaskStatus_COMPLETED},
	}
	assert.Equal(t, "[green]2 ok[-]", taskProgress(tasks))
}

func TestTaskProgress_Mixed(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Name: "t1", Status: proto.TaskStatus_COMPLETED},
		{Name: "t2", Status: proto.TaskStatus_RUNNING},
		{Name: "t3", Status: proto.TaskStatus_FAILED, ExitCode: lo.ToPtr(int32(1))},
		{Name: "t4", Status: proto.TaskStatus_QUEUED},
		{Name: "t5", Status: proto.TaskStatus_ABORTED},
	}
	result := taskProgress(tasks)
	// Order: running, completed, failed, aborted, queued
	assert.Equal(t, "[yellow]1 run[-], [green]1 ok[-], [red]1 fail[-], [gray]1 abort[-], [white]1 queue[-]", result)
}

func TestTaskProgress_OnlyRunning(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Name: "t1", Status: proto.TaskStatus_RUNNING},
		{Name: "t2", Status: proto.TaskStatus_RUNNING},
	}
	assert.Equal(t, "[yellow]2 run[-]", taskProgress(tasks))
}

func TestTaskProgress_OnlyFailed(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Name: "t1", Status: proto.TaskStatus_FAILED, ExitCode: lo.ToPtr(int32(1))},
	}
	assert.Equal(t, "[red]1 fail[-]", taskProgress(tasks))
}

func TestTaskProgress_OnlyAborted(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Name: "t1", Status: proto.TaskStatus_ABORTED},
	}
	assert.Equal(t, "[gray]1 abort[-]", taskProgress(tasks))
}

// --- nodeSlotsSummary ---

func makeSlot(job, name string) *proto.NodeStatus_Slot {
	return &proto.NodeStatus_Slot{Task: &proto.NodeStatus_Slot_Task{Job: job, Name: name}}
}

func makeEmptySlot() *proto.NodeStatus_Slot {
	return &proto.NodeStatus_Slot{}
}

func TestNodeSlotsSummary_Empty(t *testing.T) {
	busy, running := nodeSlotsSummary([]*proto.NodeStatus_Slot{makeEmptySlot(), makeEmptySlot()})
	assert.Equal(t, 0, busy)
	assert.Equal(t, "", running)
}

func TestNodeSlotsSummary_NilSlots(t *testing.T) {
	busy, running := nodeSlotsSummary([]*proto.NodeStatus_Slot{nil, nil})
	assert.Equal(t, 0, busy)
	assert.Equal(t, "", running)
}

func TestNodeSlotsSummary_SingleTask(t *testing.T) {
	busy, running := nodeSlotsSummary([]*proto.NodeStatus_Slot{
		makeSlot("job-1", "a2c"),
		makeEmptySlot(),
	})
	assert.Equal(t, 1, busy)
	assert.Equal(t, "a2c", running)
}

func TestNodeSlotsSummary_MultiSlotTask(t *testing.T) {
	// "fase" occupies 8 of 16 slots
	slots := make([]*proto.NodeStatus_Slot, 16)
	for i := 0; i < 8; i++ {
		slots[i] = makeSlot("job-1", "fase")
	}
	for i := 8; i < 16; i++ {
		slots[i] = makeEmptySlot()
	}
	busy, running := nodeSlotsSummary(slots)
	assert.Equal(t, 8, busy)
	assert.Equal(t, "fase(8)", running)
}

func TestNodeSlotsSummary_MixedTasks(t *testing.T) {
	// "fase" occupies 8 slots, "a2c" occupies 1 slot
	slots := make([]*proto.NodeStatus_Slot, 16)
	for i := 0; i < 8; i++ {
		slots[i] = makeSlot("job-1", "fase")
	}
	slots[8] = makeSlot("job-1", "a2c")
	for i := 9; i < 16; i++ {
		slots[i] = makeEmptySlot()
	}
	busy, running := nodeSlotsSummary(slots)
	assert.Equal(t, 9, busy)
	assert.Equal(t, "fase(8) a2c", running)
}

func TestNodeSlotsSummary_TwoSingleSlotTasks(t *testing.T) {
	busy, running := nodeSlotsSummary([]*proto.NodeStatus_Slot{
		makeSlot("job-1", "task-a"),
		makeSlot("job-1", "task-b"),
	})
	assert.Equal(t, 2, busy)
	assert.Equal(t, "task-a task-b", running)
}

func TestNodeSlotsSummary_SameTaskDifferentJobs(t *testing.T) {
	// Same task name from different jobs should be shown separately
	busy, running := nodeSlotsSummary([]*proto.NodeStatus_Slot{
		makeSlot("job-1", "build"),
		makeSlot("job-2", "build"),
	})
	assert.Equal(t, 2, busy)
	assert.Equal(t, "build build", running)
}

func TestNodeSlotsSummary_FullNode(t *testing.T) {
	// All 4 slots occupied: 3 by "fase", 1 by "a2c"
	slots := []*proto.NodeStatus_Slot{
		makeSlot("job-1", "fase"),
		makeSlot("job-1", "fase"),
		makeSlot("job-1", "fase"),
		makeSlot("job-1", "a2c"),
	}
	busy, running := nodeSlotsSummary(slots)
	assert.Equal(t, 4, busy)
	assert.Equal(t, fmt.Sprintf("fase(3) a2c"), running)
}
