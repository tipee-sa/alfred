package internal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/scheduler"
	"github.com/samber/lo"
)

func RunContainer(ctx context.Context, log *slog.Logger, docker *client.Client, task *scheduler.Task, fs WorkspaceFS) (int, error) {
	cleanup := func(what string, thunk func() error, args ...any) {
		if err := thunk(); err != nil {
			args = append([]any{"error", err}, args...)
			log.Error("Failed to cleanup "+what, args...)
		}
	}

	// Setup network to link main container with services
	networkName := fmt.Sprintf("alfred-%s", task.FQN())
	netResp, err := docker.NetworkCreate(ctx, networkName, types.NetworkCreate{Driver: "bridge"})
	if err != nil {
		return -1, fmt.Errorf("failed to create network: %w", err)
	}
	networkId := netResp.ID
	defer cleanup(
		"network",
		func() error { return docker.NetworkRemove(context.Background(), networkId) },
	)

	// Initialize workspace
	taskFs := fs.Scope(task.FQN())

	if err := taskFs.MkDir("/"); err != nil {
		return -1, fmt.Errorf("failed to create workspace: %w", err)
	}
	defer cleanup(
		"workspace",
		func() error { return taskFs.Delete("/") },
	)

	for _, dir := range []string{"output"} {
		if err := taskFs.MkDir("/" + dir); err != nil {
			return -1, fmt.Errorf("failed to create workspace directory '%s': %w", dir, err)
		}
	}

	// Create main container
	resp, err := docker.ContainerCreate(
		ctx,
		&container.Config{
			Image: task.Job.Image,
			Env: append(
				lo.Map(task.Job.Env, func(jobEnv *proto.Job_Env, _ int) string {
					return fmt.Sprintf("%s=%s", jobEnv.Key, jobEnv.Value)
				}),
				[]string{
					fmt.Sprintf("ALFRED_TASK=%s", task.Name),
					"ALFRED_OUTPUT=/alfred/output",
				}...,
			),
		},
		&container.HostConfig{
			AutoRemove: false, // Otherwise this will remove the container before we can get the logs
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: taskFs.HostPath("/"),
					Target: "/alfred",
				},
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {
					NetworkID: networkId,
				},
			},
		},
		nil,
		fmt.Sprintf("alfred-%s", task.FQN()),
	)
	if err != nil {
		return -1, fmt.Errorf("failed to create container: %w", err)
	}
	defer cleanup(
		"main container",
		func() error {
			return docker.ContainerRemove(context.Background(), resp.ID,
				types.ContainerRemoveOptions{RemoveVolumes: true, Force: true})
		},
	)

	// Environment variables for each service
	serviceEnv := map[string][]string{}
	// Container IDs for each service
	serviceContainers := map[string]string{}

	for _, service := range task.Job.Services {
		serviceLog := log.With("service", service.Name)

		env := lo.Map(service.Env, func(jobEnv *proto.Job_Env, _ int) string {
			return fmt.Sprintf("%s=%s", jobEnv.Key, jobEnv.Value)
		})
		serviceEnv[service.Name] = env

		// Make sure the image has been loaded
		list, err := docker.ImageList(ctx, types.ImageListOptions{
			Filters: filters.NewArgs(filters.Arg("reference", service.Image)),
		})
		if err != nil {
			return -1, fmt.Errorf("failed to list images for service '%s': %w", service.Name, err)
		}

		// We only need to check that the list is non-empty, because we filtered by reference
		if len(list) == 0 {
			serviceLog.Debug("Pulling service image")
			reader, err := docker.ImagePull(ctx, service.Image, types.ImagePullOptions{})
			if err != nil {
				return -1, fmt.Errorf("failed to pull image for service '%s': %w", service.Name, err)
			}
			defer reader.Close()

			// Wait for the pull to finish
			_, _ = io.Copy(io.Discard, reader)

			// We might not be handling pull error properly, but parsing the JSON response is a pain
			// Let's just assume it worked, and if it didn't, the container create will fail
		}

		resp, err := docker.ContainerCreate(
			ctx,
			&container.Config{
				Image: service.Image,
				Env:   env,
			},
			&container.HostConfig{},
			&network.NetworkingConfig{
				EndpointsConfig: map[string]*network.EndpointSettings{
					networkName: {
						NetworkID: networkId,
						Aliases:   []string{service.Name},
					},
				},
			},
			nil,
			fmt.Sprintf("alfred-%s-%s", task.FQN(), service.Name),
		)
		if err != nil {
			return -1, fmt.Errorf("failed to create container for service '%s': %w", service.Name, err)
		}
		defer cleanup(
			"service container",
			func() error {
				return docker.ContainerRemove(context.Background(), resp.ID,
					types.ContainerRemoveOptions{RemoveVolumes: true, Force: true})
			},
			"service", service.Name,
		)
		serviceContainers[service.Name] = resp.ID
	}

	// Start all services
	var wg sync.WaitGroup
	serviceErrors := make(chan error, len(task.Job.Services))

	wg.Add(len(task.Job.Services))
	for _, service := range task.Job.Services {
		go func(service *proto.Job_Service) {
			defer wg.Done()
			serviceLog := log.With("service", service.Name)

			serviceLog.Debug("Starting service container")
			containerId := serviceContainers[service.Name]
			err := docker.ContainerStart(ctx, containerId, types.ContainerStartOptions{})
			if err != nil {
				serviceErrors <- fmt.Errorf("%s: failed to start container: %w", service.Name, err)
				return
			}

			if service.Health == nil {
				serviceLog.Debug("No health check defined, skipping...")
				return
			}

			interval := lo.Ternary(service.Health.Interval != nil, service.Health.Interval.AsDuration(), 10*time.Second)
			timeout := lo.Ternary(service.Health.Timeout != nil, service.Health.Timeout.AsDuration(), 5*time.Second)
			retries := lo.Ternary(service.Health.Retries != nil, int(*service.Health.Retries), 3)

			for i := 0; i < retries; i++ {
				// Always wait 1 second before running the health check, and potentially more between retries
				time.Sleep(lo.Ternary(i > 0, interval, 1*time.Second))

				healthCheckLog := serviceLog.With(slog.Group("retry", "attempt", i+1, "interval", interval))

				exec, err := docker.ContainerExecCreate(ctx, containerId, types.ExecConfig{
					Cmd:          append([]string{service.Health.Cmd}, service.Health.Args...),
					Env:          serviceEnv[service.Name],
					AttachStdout: true, // We are piping stdout to io.Discard to "wait" for completion
				})
				if err != nil {
					serviceErrors <- fmt.Errorf("%s: failed to create exec: %w", service.Name, err)
					return
				}

				execCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				healthCheckLog.Debug("Running health check")
				attach, err := docker.ContainerExecAttach(execCtx, exec.ID, types.ExecStartCheck{})
				if err != nil {
					serviceErrors <- fmt.Errorf("%s: failed to attach to exec: %w", service.Name, err)
					return
				}

				healthCheckTimedOut := false
				go func() {
					<-execCtx.Done()
					healthCheckTimedOut = true
					attach.Close()
				}()

				if _, err := io.Copy(io.Discard, attach.Reader); err != nil && !healthCheckTimedOut {
					serviceErrors <- fmt.Errorf("%s: failed during exec: %w", service.Name, err)
					return
				}

				if !healthCheckTimedOut {
					inspect, err := docker.ContainerExecInspect(ctx, exec.ID)
					if err != nil {
						serviceErrors <- fmt.Errorf("%s: failed to inspect exec: %w", service.Name, err)
						return
					}
					if inspect.ExitCode == 0 {
						healthCheckLog.Debug("Service is ready")
						return
					}

					healthCheckLog.Debug("Service health check failed, retrying...", "exitcode", inspect.ExitCode)
				} else {
					healthCheckLog.Debug("Service health check timed out, retrying...")
				}
			}

			serviceErrors <- fmt.Errorf("%s: health check failed", service.Name)
		}(service)
	}

	// Wait for all services to start
	wg.Wait()
	close(serviceErrors)

	if err := <-serviceErrors; err != nil {
		return -1, fmt.Errorf("service: %w", err)
	}

	// Start main container
	wait, errChan := docker.ContainerWait(ctx, resp.ID, container.WaitConditionNextExit)
	err = docker.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return -1, fmt.Errorf("failed to start container: %w", err)
	}

	out, err := docker.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: true, Details: true})
	if err != nil {
		return -1, fmt.Errorf("failed to get container logs: %w", err)
	}
	defer out.Close()

	_, _ = stdcopy.StdCopy(os.Stdout, os.Stderr, out)

	// Wait for the container to finish
	select {
	case resp := <-wait:
		// Container is done
		if resp.StatusCode != 0 {
			return int(resp.StatusCode), fmt.Errorf("exited status: %d", resp.StatusCode)
		}
	case err := <-errChan:
		return -1, fmt.Errorf("failed to wait for container: %w", err)
	}

	// Copy output files
	reader, err := taskFs.Archive("/output")
	if err != nil {
		return -1, fmt.Errorf("failed to archive output: %w", err)
	}
	defer reader.Close()

	file := lo.Must(os.OpenFile("workspace/node/output.tar.gz", os.O_CREATE|os.O_WRONLY, 0644))
	_, _ = io.Copy(file, reader)
	defer file.Close()

	return 0, nil
}
