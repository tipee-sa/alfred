package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List jobs",
	Long: `Lists the jobs the server knows, one per line: when each was scheduled, who
started it, its name, its state, how many of its tasks have ended out of how
many, and how many tasks have each status.

The state is "queued" while none of the job's tasks has started, "running"
from then on, and "completed" once every task has ended, however it ended: the
outcome is in the task statuses ('alfred help scripting' lists them).

The server keeps jobs in memory, so a restart forgets them (their artifacts
stay on disk).`,
	Args: cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.WatchJobs(cmd.Context(), &proto.WatchJobsRequest{})
		if err != nil {
			return err
		}

		msg, err := c.Recv()
		if err != nil {
			return err
		}

		nameWidth := lo.Max(lo.Map(msg.Jobs, func(j *proto.JobStatus, _ int) int { return len(j.Name) }))
		for _, j := range msg.Jobs {
			cmd.Println(psLine(j, nameWidth))
		}

		return nil
	},
}

// psState returns the job's state as `ps` shows it: that of `watch --json`, except that a job
// none of whose tasks has started yet is "queued".
func psState(j *proto.JobStatus) string {
	state, _, _, _ := jobProgress(j)
	if state == "running" && lo.EveryBy(j.Tasks, func(t *proto.TaskStatus) bool { return t.Status == proto.TaskStatus_QUEUED }) {
		return "queued"
	}
	return state
}

// psLine returns the job's line in `ps`, its name padded to nameWidth so the columns after it
// line up:
//
//	2026-11-03 09:12:45 +0000 UTC  dorian        my-job-x7k2  running        1/3  1 queued, 1 running, 1 completed
func psLine(j *proto.JobStatus, nameWidth int) string {
	scheduledAt := "unknown             "
	if j.ScheduledAt != nil {
		scheduledAt = j.ScheduledAt.AsTime().Truncate(time.Second).String()
	}

	state := psState(j)
	stateColor := map[string]func(string, ...any) string{
		"queued":    color.WhiteString,
		"running":   color.HiYellowString,
		"completed": color.HiGreenString,
	}[state]

	_, processed, total, _ := jobProgress(j)
	progress := fmt.Sprintf("%d/%d", processed, total)

	line := fmt.Sprintf("%s  %-12s  %s  %s  %7s  %s", scheduledAt, j.StartedBy, color.HiCyanString("%-*s", nameWidth, j.Name), stateColor("%-9s", state), progress, taskCountsText(j.Tasks))
	return strings.TrimRight(line, " ")
}
