package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
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

		var stats string

		for {
			msg, err := c.Recv()
			if err != nil {
				if err == io.EOF {
					spinner.Success(fmt.Sprintf("Job '%s' completed (%s)", args[0], stats))
					return nil
				}
				spinner.Fail()
				return err
			}

			queued := 0
			running := 0
			aborted := 0
			failed := 0
			completed := 0

			for _, t := range msg.Tasks {
				switch t.Status {
				case proto.TaskStatus_QUEUED:
					queued += 1
				case proto.TaskStatus_RUNNING:
					running += 1
				case proto.TaskStatus_ABORTED:
					aborted += 1
				case proto.TaskStatus_FAILED:
					failed += 1
				case proto.TaskStatus_COMPLETED:
					completed += 1
				}
			}

			statItems := []string{}
			if queued > 0 {
				statItems = append(statItems, fmt.Sprintf("⏳ %d", queued))
			}
			if running > 0 {
				statItems = append(statItems, fmt.Sprintf("⚙️ %d", running))
			}
			if aborted > 0 {
				statItems = append(statItems, fmt.Sprintf("💥 %d", aborted))
			}
			if failed > 0 {
				statItems = append(statItems, fmt.Sprintf("❌ %d", failed))
			}
			if completed > 0 {
				statItems = append(statItems, fmt.Sprintf("✅ %d", completed))
			}
			if msg.CompletedAt != nil {
				statItems = append(statItems, fmt.Sprintf("⏱️ %s", msg.CompletedAt.AsTime().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Second)))
			} else {
				statItems = append(statItems, fmt.Sprintf("⏱️ %s", time.Since(msg.ScheduledAt.AsTime()).Truncate(time.Second)))
			}

			stats = strings.Join(statItems, ", ")
			spinner.UpdateMessage(fmt.Sprintf("Job '%s' running (%s)", args[0], stats))
		}
	},
}
