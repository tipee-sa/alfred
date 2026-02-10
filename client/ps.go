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
	Args:  cobra.NoArgs,

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
			if j.StartedBy != "" {
				cmd.Printf("%s  %-10s  %s\n", j.ScheduledAt.AsTime().Truncate(time.Second), j.StartedBy, color.HiCyanString(j.Name))
			} else {
				cmd.Printf("%s              %s\n", j.ScheduledAt.AsTime().Truncate(time.Second), color.HiCyanString(j.Name))
			}
		}

		return nil
	},
}
