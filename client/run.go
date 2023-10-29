package main

import (
	"fmt"
	"io"
	"os/exec"

	"github.com/gammadia/alfred/client/job"
	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var runCmd = &cobra.Command{
	Use:   "run [JOB FILE]",
	Short: "Runs a job",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		overrides := job.Overrides{
			Name:      lo.Must(cmd.Flags().GetString("name")),
			Tasks:     lo.Must(cmd.Flags().GetStringSlice("tasks")),
			SkipTasks: lo.Must(cmd.Flags().GetStringSlice("skip-tasks")),
		}

		spinner := ui.NewSpinner("Preparing job")
		j, err := job.Read(args[0], overrides)
		if err != nil {
			spinner.Fail()
			return fmt.Errorf("failed to read job from '%s': %w", args[0], err)
		} else {
			spinner.Success()
		}

		if lo.Must(cmd.Flags().GetBool("dry-run")) {
			return yaml.NewEncoder(cmd.OutOrStdout()).Encode(j)
		}

		spinner = ui.NewSpinner("Uploading image to server")
		if err = sendImageToServer(cmd, j.Image); err != nil {
			spinner.Fail()
			return fmt.Errorf("failed to send image to server: %w", err)
		} else {
			spinner.Success()
		}

		spinner = ui.NewSpinner("Scheduling job")
		r, err := client.ScheduleJob(cmd.Context(), &proto.ScheduleJobRequest{Job: j})
		if err != nil {
			spinner.Fail()
			return err
		} else {
			spinner.Success()
		}

		fmt.Printf("%+v\n", r)

		return nil
	},
}

func init() {
	runCmd.Flags().StringP("name", "n", "", "name of the job")
	runCmd.Flags().StringSliceP("tasks", "t", nil, "list of tasks to run, overrides the jobfile")
	runCmd.Flags().StringSlice("skip-tasks", nil, "skips the given tasks")

	runCmd.Flags().Bool("dry-run", false, "build then show the job without running it")
}

func sendImageToServer(cmd *cobra.Command, image string) error {
	c, err := client.LoadImage(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Recv() // Close the stream

	if err = c.Send(&proto.LoadImageMessage{
		Message: &proto.LoadImageMessage_Init_{
			Init: &proto.LoadImageMessage_Init{
				ImageId: image,
			},
		},
	}); err != nil {
		return err
	}

	resp, err := c.Recv()
	if err != nil {
		return err
	}

	switch resp.Status {
	case proto.LoadImageResponse_OK:
		// The image already exists on the server
		return nil
	case proto.LoadImageResponse_CONTINUE:
		// The image does not exist on the server, send it
		cmd := exec.Command("docker", "save", image)
		reader := lo.Must(cmd.StdoutPipe())
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("docker save: %w", err)
		}
		chunk := make([]byte, *resp.ChunkSize)
		for {
			n, err := io.ReadFull(reader, chunk)
			if err != nil && err != io.ErrUnexpectedEOF {
				if err == io.EOF {
					return c.Send(&proto.LoadImageMessage{
						Message: &proto.LoadImageMessage_Done_{
							Done: &proto.LoadImageMessage_Done{},
						},
					})
				} else {
					return fmt.Errorf("read: %w", err)
				}
			} else {
				if err = c.Send(&proto.LoadImageMessage{
					Message: &proto.LoadImageMessage_Data_{
						Data: &proto.LoadImageMessage_Data{
							Chunk:  chunk[:n],
							Length: uint32(n),
						},
					},
				}); err != nil {
					return fmt.Errorf("send: %w", err)
				}
			}
		}
	default:
		return fmt.Errorf("unexpected response status: %s", resp.Status)
	}
}
