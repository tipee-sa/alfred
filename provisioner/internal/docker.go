package internal

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gammadia/alfred/scheduler"
)

func RunContainer(ctx context.Context, docker *client.Client, task *scheduler.Task) error {
	resp, err := docker.ContainerCreate(
		ctx,
		&container.Config{
			Image: task.Job.Image,
			Env:   []string{fmt.Sprintf("ALFRED_TASK=%s", task.Name)},
		},
		&container.HostConfig{
			AutoRemove: false, // Otherwise this will remove the container before we can get the logs
		},
		&network.NetworkingConfig{},
		nil,
		fmt.Sprintf("alfred-%s-%s", task.Job.Name, task.Name),
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	defer docker.ContainerRemove(context.Background(), resp.ID, types.ContainerRemoveOptions{RemoveVolumes: true, Force: true})

	wait, errChan := docker.ContainerWait(ctx, resp.ID, container.WaitConditionNextExit)

	err = docker.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	out, err := docker.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: true, Details: true})
	if err != nil {
		return fmt.Errorf("failed to get container logs: %w", err)
	}
	defer out.Close()

	_, _ = stdcopy.StdCopy(os.Stdout, os.Stderr, out)

	// Wait for the container to finish
	select {
	case resp := <-wait:
		// Container is done
		if resp.StatusCode != 0 {
			return fmt.Errorf("exited status: %d", resp.StatusCode)
		}
	case err := <-errChan:
		return fmt.Errorf("failed to wait for container: %w", err)
	}

	return nil
}
