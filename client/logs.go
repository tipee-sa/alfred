package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:     "logs <job> <task>",
	Aliases: []string{"tail"},
	Short:   "Stream step logs for a task",
	Args:    cobra.ExactArgs(2),

	RunE: func(cmd *cobra.Command, args []string) error {
		lines, err := cmd.Flags().GetInt32("lines")
		if err != nil {
			return err
		}

		follow, err := cmd.Flags().GetBool("follow")
		if err != nil {
			return err
		}

		c, err := client.StreamTaskLogs(cmd.Context(), &proto.StreamTaskLogsRequest{
			Job:       args[0],
			Task:      args[1],
			TailLines: lines,
			Follow:    follow,
		})
		if err != nil {
			return fmt.Errorf("failed to request logs: %w", err)
		}

		var spinner *ui.Spinner
		if follow {
			spinner = ui.NewSpinner("Waiting for logs")
		}

		return recvLogsLoop(cmd.Context(), c.Recv, os.Stdout, spinner)
	},
}

// recvLogsLoop reads log chunks from the stream and writes them to w.
// Returns nil on EOF or context cancellation, error on stream failure.
// If a spinner is provided, it is stopped when the first chunk arrives.
func recvLogsLoop(ctx context.Context, recv func() (*proto.StreamTaskLogsChunk, error), w io.Writer, spinner *ui.Spinner) error {
	for {
		chunk, err := recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				if spinner != nil {
					spinner.FinalMSG = ""
					spinner.Stop()
				}
				return nil
			}
			return fmt.Errorf("failed to receive logs: %w", err)
		}

		if spinner != nil {
			spinner.FinalMSG = ""
			spinner.Stop()
			spinner = nil
		}

		w.Write(chunk.Data)
	}
}

func init() {
	logsCmd.Flags().Int32P("lines", "n", 100, "number of lines to tail from each step log")
	logsCmd.Flags().BoolP("follow", "f", false, "follow log output (like tail -f)")
}
