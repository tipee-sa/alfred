package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

var artifactCmd = &cobra.Command{
	Use:   "artifact JOB [TASK...]",
	Short: "Download artifact",
	Args:  cobra.MinimumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		spinner := ui.NewSpinner("Loading artifacts")

		job, err := getJob(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		downloadableTasks, unknownTasks, partialDownload := selectArtifactTasks(job, args[1:])

		if len(unknownTasks) > 0 {
			spinner.Warn()
			for _, task := range unknownTasks {
				cmd.PrintErrln(color.HiRedString("task '%s' does not exist in job '%s'", task, args[0]))
			}
			spinner = ui.NewSpinner("Downloading artifacts")
		}

		outputDir := lo.Must(cmd.Flags().GetString("output"))
		files, errors := downloadArtifacts(downloadableTasks, outputDir, func(task, file string) error {
			return downloadArtifact(cmd.Context(), job.Name, task, file)
		}, func(done int) {
			spinner.UpdateMessage(fmt.Sprintf("Downloading artifact (%d/%d)", done, len(downloadableTasks)))
		})

		if len(errors) > 0 {
			spinner.Warn()
			for _, msg := range errors {
				cmd.PrintErrln(color.HiRedString(msg))
			}
		} else {
			spinner.Success()
		}

		// The files written, for scripts: once the spinner is done, which
		// draws on stderr, so that the two do not mix on a terminal.
		for _, file := range files {
			cmd.Println(file)
		}

		if partialDownload {
			cmd.PrintErrln(color.HiYellowString("\nWarning: not all tasks are completed, some artifacts may be partial snapshots"))
		}

		return artifactProblems(len(unknownTasks), len(errors))
	},
}

// selectArtifactTasks returns the tasks of job to download: the requested
// ones, or all of them if none is, that have started. It also returns the
// requested tasks that job does not have, in the order requested, and whether
// some task has not finished, so that its artifact is missing or partial.
func selectArtifactTasks(job *proto.JobStatus, requestedTasks []string) (downloadable []string, unknown []string, partial bool) {
	requested := lo.Uniq(requestedTasks)
	found := map[string]bool{}

	for _, task := range job.Tasks {
		if len(requested) > 0 {
			if !lo.Contains(requested, task.Name) {
				continue
			}
			found[task.Name] = true
		}

		switch task.Status {
		case proto.TaskStatus_QUEUED:
			partial = true
		case proto.TaskStatus_RUNNING:
			partial = true
			downloadable = append(downloadable, task.Name)
		case proto.TaskStatus_FAILED, proto.TaskStatus_COMPLETED, proto.TaskStatus_ABORTED, proto.TaskStatus_TIMED_OUT:
			downloadable = append(downloadable, task.Name)
		}
	}

	unknown = lo.Filter(requested, func(task string, _ int) bool { return !found[task] })
	return downloadable, unknown, partial
}

// downloadArtifacts downloads the artifact of each task, through download, to
// <outputDir>/<task>.tar.zst. It goes on past a failure, and returns the files
// written and a message per failure. progress is called after each task.
func downloadArtifacts(tasks []string, outputDir string, download func(task, file string) error, progress func(done int)) (files []string, errors []string) {
	for i, task := range tasks {
		file := path.Join(outputDir, fmt.Sprintf("%s.tar.zst", task))
		if err := download(task, file); err != nil {
			errors = append(errors, err.Error())
		} else {
			files = append(files, file)
		}
		progress(i + 1)
	}
	return files, errors
}

// artifactProblems is the command's error, making it exit non-zero, when a
// requested task does not exist or a download failed. Each was already
// reported on its own.
func artifactProblems(unknownTasks, failedDownloads int) error {
	var problems []string
	if unknownTasks > 0 {
		problems = append(problems, fmt.Sprintf("%d requested task(s) not found", unknownTasks))
	}
	if failedDownloads > 0 {
		problems = append(problems, fmt.Sprintf("%d download(s) failed", failedDownloads))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("artifacts incomplete: %s", strings.Join(problems, ", "))
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
