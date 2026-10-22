package main

import (
	"time"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/proto"
	"github.com/spf13/cobra"
)

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List jobs",
	Long: `Lists the jobs the server knows: when each was scheduled, who started it, and
its name. The server keeps jobs in memory, so a restart forgets them (their
artifacts stay on disk).`,
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

		for _, j := range msg.Jobs {
			scheduledAt := "unknown             "
			if j.ScheduledAt != nil {
				scheduledAt = j.ScheduledAt.AsTime().Truncate(time.Second).String()
			}
			cmd.Printf("%s  %-12s  %s\n", scheduledAt, j.StartedBy, color.HiCyanString(j.Name))
		}

		return nil
	},
}
