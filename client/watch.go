package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch [JOB]",
	Short: "Watch a job execution",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.WatchJob(cmd.Context(), &proto.WatchJobRequest{Name: args[0]})
		if err != nil {
			return err
		}

		spinner := ui.NewSpinner("Waiting for job data")

		itemsPrinter := func(items []string, last bool) string {
			nbItems := len(items)
			if nbItems < 1 {
				return ""
			}

			// Try to display the first or last 20 items, as long as displaying them doesn't exceed 180 characters
			displayItems := 20
			lineLength := 180
			partial := nbItems > displayItems
			var nItems []string
			for displayItems > 0 {
				if last {
					nItems = items[max(0, nbItems-displayItems):]
				} else {
					nItems = items[:min(nbItems, displayItems)]
				}
				if len(strings.Join(nItems, " ")) <= lineLength {
					break
				}
				displayItems -= 1
				partial = true
			}

			if last {
				return fmt.Sprintf("%s%s (🧮 %d)", lo.Ternary(partial, "… ", ""), strings.Join(nItems, " "), nbItems)
			}
			return fmt.Sprintf("%s%s (🧮 %d)", strings.Join(nItems, " "), lo.Ternary(partial, " …", ""), nbItems)
		}

		var stats, timestamp string
		var tasks []string

		for {
			msg, err := c.Recv()
			if err != nil {
				if err == io.EOF {
					spinner.Success(fmt.Sprintf("Job '%s' completed (🧮 %d, %s)\n%s", args[0], len(tasks), timestamp, stats))
					return nil
				}
				spinner.Fail()
				return err
			}

			queued := []string{}
			running := []string{}
			aborted := []string{}
			failed := []string{}
			completed := []string{}

			tasks = lo.Map(msg.Tasks, func(t *proto.TaskStatus, _ int) string { return t.Name })
			for _, t := range msg.Tasks {
				switch t.Status {
				case proto.TaskStatus_QUEUED:
					queued = append(queued, t.Name)
				case proto.TaskStatus_RUNNING:
					running = append(running, t.Name)
				case proto.TaskStatus_ABORTED:
					aborted = append(aborted, t.Name)
				case proto.TaskStatus_FAILED:
					failed = append(failed, t.Name)
				case proto.TaskStatus_COMPLETED:
					completed = append(completed, t.Name)
				}
			}

			statItems := []string{}
			if len(queued) > 0 {
				statItems = append(statItems, fmt.Sprintf("⏳ %s", itemsPrinter(queued, false)))
			}
			if len(running) > 0 {
				statItems = append(statItems, fmt.Sprintf("⚙️  %s", itemsPrinter(running, false)))
			}
			if len(aborted) > 0 {
				statItems = append(statItems, fmt.Sprintf("💥 %s", itemsPrinter(aborted, true)))
			}
			if len(failed) > 0 {
				statItems = append(statItems, fmt.Sprintf("❌ %s", itemsPrinter(failed, true)))
			}
			if len(completed) > 0 {
				statItems = append(statItems, fmt.Sprintf("✅ %s", itemsPrinter(completed, true)))
			}

			stats = strings.Join(statItems, "\n")

			if msg.CompletedAt != nil {
				timestamp = fmt.Sprintf("🏁 %s", msg.CompletedAt.AsTime().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Second))
			} else {
				timestamp = fmt.Sprintf("⏱️  %s", time.Since(msg.ScheduledAt.AsTime()).Truncate(time.Second))
			}

			spinner.UpdateMessage(fmt.Sprintf("Job '%s' running (🧮 %d, %s)\n%s", args[0], len(tasks), timestamp, stats))
		}
	},
}
