package main

import (
	"github.com/fatih/color"
	"github.com/gammadia/alfred/proto"
	"github.com/spf13/cobra"
)

var cancelCmd = &cobra.Command{
	Use:   "cancel JOB [TASK]",
	Short: "Cancel a running or queued job/task",
	Args:  cobra.RangeArgs(1, 2),

	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			if _, err := client.CancelJob(cmd.Context(), &proto.CancelJobRequest{Name: args[0]}); err != nil {
				return err
			}
			cmd.PrintErrln(color.HiGreenString("Cancelled job '%s'", args[0]))
		} else {
			if _, err := client.CancelTask(cmd.Context(), &proto.CancelTaskRequest{Job: args[0], Task: args[1]}); err != nil {
				return err
			}
			cmd.PrintErrln(color.HiGreenString("Cancelled task '%s' of job '%s'", args[1], args[0]))
		}
		return nil
	},
}
