package local

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gammadia/alfred/scheduler"
)

type LocalNode struct {
	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client

	nodeNumber int
}

// LocalNode implements scheduler.Node
var _ scheduler.Node = (*LocalNode)(nil)

func (ln *LocalNode) Name() string {
	return fmt.Sprintf("local-%d", ln.nodeNumber)
}

func (ln *LocalNode) Run(task *scheduler.Task) error {
	image := task.Job.Image

	list, err := ln.docker.ImageList(ln.ctx, types.ImageListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", image)),
	})

	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	// We only need to check that the list is non-empty, because we filtered by reference
	if len(list) == 0 {
		reader, err := ln.docker.ImagePull(ln.ctx, image, types.ImagePullOptions{})
		if err != nil {
			return fmt.Errorf("failed to pull image: %w", err)
		}
		defer reader.Close()

		// Wait for the pull to finish
		_, _ = io.Copy(io.Discard, reader)

		// We might not be handling pull error properly, but parsing the JSON response is a pain
		// Let's just assume it worked, and if it didn't, the container create will fail
	}

	resp, err := ln.docker.ContainerCreate(
		ln.ctx,
		&container.Config{
			Image: image,
			Cmd:   []string{"sh", "-c", "echo \"Hello $TASK_NAME!\""},
			Env:   []string{fmt.Sprintf("TASK_NAME=%s", task.Name)},
		},
		&container.HostConfig{
			AutoRemove: true,
		},
		&network.NetworkingConfig{},
		nil,
		fmt.Sprintf("alfred-%s-%s", task.Job.Name, task.Name),
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	err = ln.docker.ContainerStart(ln.ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	out, err := ln.docker.ContainerLogs(ln.ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true, Follow: true})
	if err != nil {
		return fmt.Errorf("failed to get container logs: %w", err)
	}
	defer out.Close()

	_, _ = stdcopy.StdCopy(os.Stdout, os.Stderr, out)

	wait, errChan := ln.docker.ContainerWait(ln.ctx, resp.ID, container.WaitConditionRemoved)
	select {
	case <-wait:
		// Container is done
	case err := <-errChan:
		return fmt.Errorf("failed to wait for container: %w", err)
	}

	return nil
}

func (*LocalNode) Terminate() error {
	return nil
}
