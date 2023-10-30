package internal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/scheduler"
	"github.com/samber/lo"
)

func RunContainer(ctx context.Context, log *slog.Logger, docker *client.Client, task *scheduler.Task) error {
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
		return fmt.Errorf("failed to create network: %w", err)
	}
	networkId := netResp.ID
	defer cleanup(
		"network",
		func() error { return docker.NetworkRemove(context.Background(), networkId) },
	)

	resp, err := docker.ContainerCreate(
		ctx,
		&container.Config{
			Image: task.Job.Image,
			Env:   []string{fmt.Sprintf("ALFRED_TASK=%s", task.Name)},
		},
		&container.HostConfig{
			AutoRemove: false, // Otherwise this will remove the container before we can get the logs
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
		return fmt.Errorf("failed to create container: %w", err)
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
		env := lo.Map(service.Env, func(jobEnv *proto.Job_Env, _ int) string {
			return fmt.Sprintf("%s=%s", jobEnv.Key, jobEnv.Value)
		})
		serviceEnv[service.Name] = env

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
			return fmt.Errorf("failed to create container for service '%s': %w", service.Name, err)
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

			// TODO: move to job definition
			interval := 10 * time.Second
			timeout := 5 * time.Second
			retries := 3

			for i := 0; i < retries; i++ {
				time.Sleep(interval)
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
		return fmt.Errorf("service: %w", err)
	}

	// Start main container
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
