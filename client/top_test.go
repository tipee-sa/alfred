package main

import (
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
