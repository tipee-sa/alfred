package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Download artifact",
	Args:  cobra.MinimumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		spinner := ui.NewSpinner("Loading artifacts")

		job, err := getJob(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		partialDownload := false

		requestedTasks := args[1:]
		var invalidRequestedTasks = make(map[string]bool, len(requestedTasks))
		for _, requestedTask := range requestedTasks {
			invalidRequestedTasks[requestedTask] = true
		}

		finishedTasks := []string{}

		for _, task := range job.Tasks {
			if len(requestedTasks) > 0 {
				if _, exists := invalidRequestedTasks[task.Name]; exists {
					delete(invalidRequestedTasks, task.Name)
				} else {
					continue
				}
			}

			switch task.Status {
			case proto.TaskStatus_QUEUED, proto.TaskStatus_RUNNING, proto.TaskStatus_ABORTED:
				partialDownload = true
			case proto.TaskStatus_FAILED, proto.TaskStatus_COMPLETED:
				finishedTasks = append(finishedTasks, task.Name)
			}
		}

		if len(requestedTasks) > 0 && len(invalidRequestedTasks) > 0 {
			spinner.Warn()
			for invalidTask := range invalidRequestedTasks {
				cmd.PrintErrln(color.HiRedString("task '%s' does not exist in job '%s'", invalidTask, args[0]))
			}
			spinner = ui.NewSpinner("Downloading artifacts")
		}

		var errors []string
		for i, task := range finishedTasks {
			file := path.Join(lo.Must(cmd.Flags().GetString("output")), fmt.Sprintf("%s.tar.zst", task))
			if err := downloadArtifact(cmd.Context(), job.Name, task, file); err != nil {
				errors = append(errors, err.Error())
			}

			spinner.UpdateMessage(fmt.Sprintf("Downloading artifact (%d/%d)", i+1, len(finishedTasks)))
		}

		if len(errors) > 0 {
			spinner.Warn()
			for _, msg := range errors {
				cmd.PrintErrln(color.HiRedString(msg))
			}
		} else {
			spinner.Success()
		}

		if partialDownload {
			cmd.PrintErrln(color.HiYellowString("\nWarning: not all tasks are completed, only some artifacts were downloaded"))
		}

		return nil
	},
}

func init() {
	artifactCmd.Flags().StringP("output", "o", "", "output directory")
	lo.Must0(artifactCmd.MarkFlagRequired("output"))
}

func getJob(ctx context.Context, name string) (*proto.JobStatus, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c, err := client.WatchJob(ctx, &proto.WatchJobRequest{Name: name})
	if err != nil {
		return nil, err
	}

	return c.Recv()
}

func downloadArtifact(ctx context.Context, job string, task string, file string) (err error) {
	if err = os.MkdirAll(path.Dir(file), 0755); err != nil {
		return fmt.Errorf("failed to create output directory for task '%s': %w", task, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c, err := client.DownloadArtifact(ctx, &proto.DownloadArtifactRequest{
		Job:  job,
		Task: task,
	})
	if err != nil {
		return fmt.Errorf("failed to initiate artifact download for task '%s': %w", task, err)
	}

	fd, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create output file for task '%s': %w", task, err)
	}
	defer func() {
		fd.Close()
		if err != nil {
			_ = os.Remove(file)
		}
	}()

	for {
		chunk, err := c.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to download artifact for task '%s': %w", task, err)
		}

		_, err = io.Copy(fd, bytes.NewReader(chunk.Data))
		if err != nil {
			return fmt.Errorf("failed to write artifact for task '%s': %w", task, err)
		}
	}
}
