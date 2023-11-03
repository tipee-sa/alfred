package main

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/client/jobfile"
	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var runCmd = &cobra.Command{
	Use:   "run [JOBFILE] [ARGS...]",
	Short: "Runs a job",
	Args:  cobra.MinimumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		var spinner *ui.Spinner
		if !verbose {
			spinner = ui.NewSpinner("Preparing job")
		} else {
			cmd.PrintErrln(ui.SectionHeaderColor.Sprint("  Preparing job  "))
		}
		j, err := jobfile.Read(args[0], jobfile.ReadOptions{
			Verbose: verbose,
			Args:    args[1:],
			Params:  lo.SliceToMap(lo.Must(cmd.Flags().GetStringArray("param")), func(item string) (key, value string) { key, value, _ = strings.Cut(item, "="); return }),
		})
		if err != nil {
			spinner.Fail()
			if e, ok := err.(jobfile.UnmarshalError); ok && verbose {
				cmd.PrintErrln(e.Source)
			}
			return fmt.Errorf("failed to read job from '%s': %w", args[0], err)
		} else {
			spinner.Success()
		}

		if lo.Must(cmd.Flags().GetBool("dry-run")) {
			cmd.Println()
			cmd.Println(ui.SectionHeaderColor.Sprint("  Jobfile  "))
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
		job, err := client.ScheduleJob(cmd.Context(), &proto.ScheduleJobRequest{Job: j})
		if err != nil {
			spinner.Fail()
			return err
		} else {
			spinner.Success()
		}

		if !lo.Must(cmd.Flags().GetBool("async")) {
			if err := watchCmd.RunE(cmd, []string{job.Name}); err != nil {
				return err
			}
		} else {
			cmd.Printf(color.HiGreenString("Scheduled job '%s'\n"), job.Name)
		}

		return nil
	},
}

func init() {
	runCmd.Flags().Bool("async", false, "run the job asynchronously")
	runCmd.Flags().BoolP("dry-run", "n", false, "build then show the job without running it")
	runCmd.Flags().StringArrayP("param", "p", nil, "jobfile parameters to set")
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
