package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:     "logs JOB TASK",
	Aliases: []string{"tail"},
	Short:   "Stream step logs for a task",
	Args:    cobra.ExactArgs(2),

	RunE: func(cmd *cobra.Command, args []string) error {
		follow, err := cmd.Flags().GetBool("follow")
		if err != nil {
			return err
		}

		c, err := client.StreamTaskLogs(cmd.Context(), &proto.StreamTaskLogsRequest{
			Job:       args[0],
			Task:      args[1],
			TailLines: int32(logsTailLines),
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

// allTailLines is the TailLines value asking the server for whole logs.
// Zero cannot say it: proto3 does not tell 0 from an unset field, and the
// server reads both as its default of 100 lines. A server older than "all"
// treats a negative value the same way, so it degrades to 100 lines.
const allTailLines = -1

// parseTailLines parses the --lines flag: a positive number of lines, or "all".
func parseTailLines(value string) (int32, error) {
	if value == "all" {
		return allTailLines, nil
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer or \"all\"")
	}
	return int32(n), nil
}

// tailLinesValue is the --lines flag. As a flag type, it is validated while
// flags are parsed, before the connection to the server is set up.
type tailLinesValue int32

var logsTailLines tailLinesValue = 100

func (v *tailLinesValue) String() string {
	if *v == allTailLines {
		return "all"
	}
	return strconv.Itoa(int(*v))
}

func (v *tailLinesValue) Set(value string) error {
	n, err := parseTailLines(value)
	if err != nil {
		return err
	}
	*v = tailLinesValue(n)
	return nil
}

func (v *tailLinesValue) Type() string { return "int|all" }

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
	logsCmd.Flags().VarP(&logsTailLines, "lines", "n", "number of lines to show from the end of each step log, or \"all\" (at most 10000 with -f while the task runs)")
	logsCmd.Flags().BoolP("follow", "f", false, "follow log output (like tail -f)")
}
