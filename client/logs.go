package main

import (
	"fmt"
	"io"
	"os"

	"github.com/gammadia/alfred/proto"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs <job> <task>",
	Short: "Stream step logs for a task",
	Args:  cobra.ExactArgs(2),

	RunE: func(cmd *cobra.Command, args []string) error {
		lines, err := cmd.Flags().GetInt32("lines")
		if err != nil {
			return err
		}

		c, err := client.StreamTaskLogs(cmd.Context(), &proto.StreamTaskLogsRequest{
			Job:       args[0],
			Task:      args[1],
			TailLines: lines,
		})
		if err != nil {
			return fmt.Errorf("failed to request logs: %w", err)
		}

		for {
			chunk, err := c.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return fmt.Errorf("failed to receive logs: %w", err)
			}

			os.Stdout.Write(chunk.Data)
		}
	},
}

func init() {
	logsCmd.Flags().Int32P("lines", "n", 100, "number of lines to tail from each step log")
}
