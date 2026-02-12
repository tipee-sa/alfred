package internal

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/scheduler"
	"github.com/samber/lo"
)

// RunContainer is the core task execution engine. It orchestrates the full lifecycle:
// network → workspace → services (parallel) → steps (sequential) → artifact archival → cleanup.
//
// Cleanup uses deferred calls in LIFO order. The order of defer registration matters:
//   1. defer: remove network         (registered first → runs last)
//   2. defer: remove workspace       (registered second → runs second-to-last)
//   3. defer: OnWorkspaceTeardown    (unregisters live archiver)
//   4. defer: remove service containers (one defer per service)
//   5. (per step) defer: remove step container + cancel log streaming
//
// Artifact archival happens BEFORE workspace deletion (it's not deferred — it runs inline
// after all steps). This is critical: archiving reads from /output which lives in the workspace.
// imageOverrides maps original image references (SHAs) to tagged references for nodes where
// containerd image store requires tagged references for loaded images.
func RunContainer(
	ctx context.Context,
	docker *client.Client,
	task *scheduler.Task,
	fs WorkspaceFS,
	runConfig scheduler.RunTaskConfig,
	imageOverrides map[string]string,
) (int, error) {
	// tryTo is a best-effort cleanup helper: logs errors but doesn't fail the task.
	// Used in defers because cleanup should not mask the original error.
	tryTo := func(what string, thunk func() error, args ...any) {
		if err := thunk(); err != nil {
			args = append([]any{"error", err}, args...)
			task.Log.Error("Failed to "+what, args...)
		}
	}

	// Setup network to link main container with services
	networkName := fmt.Sprintf("alfred-%s", task.FQN())
	netResp, err := docker.NetworkCreate(ctx, networkName, network.CreateOptions{Driver: "bridge"})
	if err != nil {
		return -1, fmt.Errorf("failed to create docker network: %w", err)
	}
	networkId := netResp.ID
	// Uses context.Background() so cleanup isn't skipped if ctx is already cancelled
	defer tryTo(
		"remove Docker network",
		func() error {
			return docker.NetworkRemove(context.Background(), networkId)
		},
	)

	// Initialize workspace
	taskFs := fs.Scope(task.FQN())

	if err := taskFs.MkDir("/"); err != nil {
		return -1, fmt.Errorf("failed to create workspace: %w", err)
	}
	defer tryTo(
		"remove task workspace",
		func() error {
			return taskFs.Delete("/")
		},
	)

	for _, dir := range []string{"output", "shared"} {
		if err := taskFs.MkDir("/" + dir); err != nil {
			return -1, fmt.Errorf("failed to create workspace directory '%s': %w", dir, err)
		}
	}

	// Register live artifact archiver for in-progress downloads
	if runConfig.OnWorkspaceReady != nil {
		runConfig.OnWorkspaceReady(func() (io.ReadCloser, error) {
			return taskFs.Archive("/output")
		})
	}
	if runConfig.OnLogReaderReady != nil {
		runConfig.OnLogReaderReady(func(lines int) (io.ReadCloser, error) {
			return taskFs.TailLogs("/output", lines)
		})
	}
	if runConfig.OnWorkspaceTeardown != nil {
		defer runConfig.OnWorkspaceTeardown()
	}

	// Environment variables for each service
	serviceEnv := map[string][]string{}
	// Container IDs for each service
	serviceContainers := map[string]string{}

	for _, service := range task.Job.Services {
		serviceLog := task.Log.With("service", service.Name)

		env := lo.Map(service.Env, func(jobEnv *proto.Job_Env, _ int) string {
			return fmt.Sprintf("%s=%s", jobEnv.Key, jobEnv.Value)
		})
		serviceEnv[service.Name] = env

		// Make sure the image has been loaded
		list, err := docker.ImageList(ctx, image.ListOptions{
			Filters: filters.NewArgs(filters.Arg("reference", service.Image)),
		})
		if err != nil {
			return -1, fmt.Errorf("failed to list docker images for service '%s': %w", service.Name, err)
		}

		// We only need to check that the list is non-empty, because we filtered by reference
		if len(list) == 0 {
			serviceLog.Debug("Pulling service image")
			reader, err := docker.ImagePull(ctx, service.Image, image.PullOptions{})
			if err != nil {
				return -1, fmt.Errorf("failed to pull docker image for service '%s': %w", service.Name, err)
			}

			// Wait for the pull to finish, then close immediately (not deferred,
			// since we're inside a loop and don't want to accumulate open readers)
			_, _ = io.Copy(io.Discard, reader)
			reader.Close()

			// We might not be handling pull error properly, but parsing the JSON response is a pain
			// Let's just assume it worked, and if it didn't, the container create will fail
		} else {
			serviceLog.Debug("Service image already on node")
		}

		tmpfs := map[string]string{}
		for _, t := range service.Tmpfs {
			path, opts, _ := strings.Cut(t, ":")
			tmpfs[path] = opts
		}

		resp, err := docker.ContainerCreate(
			ctx,
			&container.Config{
				Image: service.Image,
				Cmd:   service.Command,
				Env:   env,
			},
			&container.HostConfig{
				Tmpfs: tmpfs,
			},
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
			return -1, fmt.Errorf("failed to create docker container for service '%s': %w", service.Name, err)
		}
		defer tryTo(
			"remove service container",
			func() error {
				return docker.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{RemoveVolumes: true, Force: true})
			},
			"service", service.Name,
		)
		serviceContainers[service.Name] = resp.ID
	}

	// Start all services concurrently. Each service gets its own goroutine that blocks
	// on startAndWaitForService (which starts the container and runs health check retries).
	// WaitGroup ensures we don't proceed to step execution until all services are ready.
	var wg sync.WaitGroup
	// Buffered to len(services) so goroutines never block on send
	serviceErrors := make(chan error, len(task.Job.Services))

	wg.Add(len(task.Job.Services))
	for _, service := range task.Job.Services {
		go func() {
			defer wg.Done()
			serviceLog := task.Log.With("service", service.Name)
			containerId := serviceContainers[service.Name]

			if err := startAndWaitForService(ctx, docker, service, containerId, serviceEnv[service.Name], serviceLog); err != nil {
				// Fetch container logs to help diagnose the failure
				logReader, logErr := docker.ContainerLogs(ctx, containerId, container.LogsOptions{
					ShowStdout: true,
					ShowStderr: true,
				})
				if logErr == nil {
					if logs, readErr := io.ReadAll(logReader); readErr == nil && len(logs) > 0 {
						serviceLog.Warn("Service container logs", "logs", string(logs))
					}
					logReader.Close()
				}

				serviceErrors <- err
			}
		}()
	}

	wg.Wait()          // block until all service goroutines finish
	close(serviceErrors) // safe to close: all senders are done

	if err := <-serviceErrors; err != nil {
		return -1, fmt.Errorf("some service failed: %w", err)
	}

	// Create and execute step containers sequentially.
	// Each step runs in an immediately-invoked closure so that defer statements (container
	// removal, log cancellation) execute between iterations instead of accumulating until
	// RunContainer returns.
	var status container.WaitResponse
	var stepError error
	for i, image := range task.Job.Steps {
		if override, ok := imageOverrides[image]; ok {
			image = override
		}
		stepError = func(stepIndex int) error {
			secretEnv := []string{}
			for _, secret := range task.Job.Secrets {
				if runConfig.SecretLoader == nil {
					return fmt.Errorf("no secret loader available")
				}
				secretData, err := runConfig.SecretLoader(secret.Value)
				if err != nil {
					return fmt.Errorf("failed to load secret '%s': %w", secret.Key, err)
				}
				secretEnv = append(secretEnv, fmt.Sprintf("%s=%s", secret.Key, base64.StdEncoding.EncodeToString(secretData)))
			}

			resp, err := docker.ContainerCreate(
				ctx,
				&container.Config{
					Image: image,
					Env: append(
						append(
							lo.Map(task.Job.Env, func(jobEnv *proto.Job_Env, _ int) string {
								return fmt.Sprintf("%s=%s", jobEnv.Key, jobEnv.Value)
							}),
							secretEnv...,
						),
						[]string{
							fmt.Sprintf("ALFRED_TASK=%s", task.Name),
							fmt.Sprintf("ALFRED_TASK_FQN=%s", task.FQN()),
							"ALFRED_SHARED=/alfred/shared",
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
				fmt.Sprintf("alfred-%s-%d", task.FQN(), stepIndex),
			)
			if err != nil {
				return fmt.Errorf("failed to create docker container for step %d: %w", stepIndex, err)
			}
			defer tryTo(
				"remove step container",
				func() error {
					return docker.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{RemoveVolumes: true, Force: true})
				},
				"step", stepIndex,
			)

			// ContainerWait returns two channels: wait (exit status) and errChan (Docker API error).
			// We register the wait BEFORE starting so we don't miss the exit event.
			wait, errChan := docker.ContainerWait(ctx, resp.ID, container.WaitConditionNextExit)
			err = docker.ContainerStart(ctx, resp.ID, container.StartOptions{})
			if err != nil {
				return fmt.Errorf("failed to start docker container for step %d: %w", stepIndex, err)
			}

			// Stream container logs to file in a background goroutine.
			// Uses a child context so we can cancel log streaming when the container exits.
			// logDone channel signals when the goroutine finishes (buffered so it never blocks).
			logPath := fmt.Sprintf("/output/%s-step-%d.log", task.Name, stepIndex)
			logCtx, cancelLogs := context.WithCancel(ctx)
			defer cancelLogs()
			logDone := make(chan error, 1)
			go func() {
				logDone <- taskFs.StreamContainerLogs(logCtx, resp.ID, logPath)
			}()

			// Wait for the container to finish. Three possible outcomes:
			// 1. Container exits normally → wait receives the exit status
			// 2. Docker API error → errChan receives the error
			// 3. Context cancelled (task aborted) → ctx.Done() fires
			select {
			case status = <-wait:
				// Container exited. Wait for log streaming goroutine to flush remaining output.
				// Only report log errors if they're genuine (not from our own cancellation).
				if err := <-logDone; err != nil && logCtx.Err() == nil {
					task.Log.Error("Failed to stream container logs", "error", err, "step", stepIndex)
				}

				if status.StatusCode != 0 {
					return fmt.Errorf("step %d failed with status: %d", stepIndex, status.StatusCode)
				}
			case err := <-errChan:
				// Docker API failed. Cancel log streaming and wait for the goroutine to exit
				// (prevents goroutine leak).
				cancelLogs()
				<-logDone
				return fmt.Errorf("failed while waiting for docker container for step %d: %w", stepIndex, err)
			case <-ctx.Done():
				// Task was cancelled. Actively kill the container since context cancellation
				// may not propagate through the Docker SDK (especially over SSH-tunneled connections).
				task.Log.Info("Killing container for aborted step", "step", stepIndex)
				_ = docker.ContainerKill(context.Background(), resp.ID, "SIGKILL")
				cancelLogs()
				<-logDone
				return fmt.Errorf("step %d aborted: %w", stepIndex, ctx.Err())
			}

			return nil
		}(i + 1)

		// There's no point in executing further steps if one of them failed
		if stepError != nil {
			break
		}
	}

	// Archive artifacts BEFORE returning (which triggers deferred workspace deletion).
	// This is NOT deferred because it must run before the workspace defer that deletes /output.
	// Skip archival when the task was aborted — the archive operation could also block
	// over SSH-tunneled connections, and partial artifacts from aborted tasks aren't useful.
	if ctx.Err() == nil {
		tryTo(
			"preserve artifact",
			func() error {
				if runConfig.ArtifactPreserver != nil {
					task.Log.Debug("Preserve artifact")

					reader, err := taskFs.Archive("/output")
					if err != nil {
						return fmt.Errorf("failed to archive 'output' directory: %w", err)
					}
					defer reader.Close()

					if err := runConfig.ArtifactPreserver(reader, task); err != nil {
						return fmt.Errorf("failed to preserve artifacts: %w", err)
					}
				}
				return nil
			},
		)
	}

	if stepError != nil {
		return lo.Ternary(status.StatusCode != 0, int(status.StatusCode), -1), fmt.Errorf("task execution ended with error: %w", stepError)
	}

	return 0, nil
}

func startAndWaitForService(
	ctx context.Context,
	docker *client.Client,
	service *proto.Job_Service,
	containerId string,
	env []string,
	serviceLog *slog.Logger,
) error {
	serviceLog.Debug("Starting service container")
	err := docker.ContainerStart(ctx, containerId, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start docker container for service '%s': %w", service.Name, err)
	}

	if service.Health == nil {
		serviceLog.Debug("No health check defined, skipping...")
		return nil
	}

	interval := lo.Ternary(service.Health.Interval != nil, service.Health.Interval.AsDuration(), 10*time.Second)
	timeout := lo.Ternary(service.Health.Timeout != nil, service.Health.Timeout.AsDuration(), 5*time.Second)
	retries := lo.Ternary(service.Health.Retries != nil, int(*service.Health.Retries), 3)

	for i := 0; i < retries; i++ {
		// Always wait 1 second before running the health check, and potentially more between retries
		time.Sleep(lo.Ternary(i > 0, interval, 1*time.Second))

		healthCheckLog := serviceLog.With(slog.Group("retry", "attempt", i+1, "interval", interval))
		healthCheckCmd := append([]string{service.Health.Cmd}, service.Health.Args...)

		exec, err := docker.ContainerExecCreate(ctx, containerId, container.ExecOptions{
			Cmd:          healthCheckCmd,
			Env:          env,
			AttachStdout: true, // We are piping stdout to io.Discard to "wait" for completion
		})
		if err != nil {
			return fmt.Errorf("failed to create docker exec for service '%s': %w", service.Name, err)
		}

		execCtx, cancel := context.WithTimeout(ctx, timeout)
		healthCheckLog.Debug("Running health check", "cmd", healthCheckCmd)
		attach, err := docker.ContainerExecAttach(execCtx, exec.ID, container.ExecStartOptions{})
		if err != nil {
			cancel()
			return fmt.Errorf("failed to attach docker exec for service '%s': %w", service.Name, err)
		}

		// Monitor goroutine: when the timeout fires, close the attach reader to unblock
		// io.Copy below. Without this, io.Copy would block indefinitely if the health
		// check command hangs. The atomic bool distinguishes timeout-induced errors from real ones.
		var healthCheckTimedOut atomic.Bool
		go func() {
			<-execCtx.Done()             // blocks until timeout or parent cancellation
			healthCheckTimedOut.Store(true)
			attach.Close()               // forces io.Copy to return with an error
		}()

		// Drain stdout to "wait" for the exec to complete. If the monitor goroutine
		// closed the reader due to timeout, err is non-nil but we ignore it.
		if _, err := io.Copy(io.Discard, attach.Reader); err != nil && !healthCheckTimedOut.Load() {
			cancel()
			return fmt.Errorf("failed during docker exec for service '%s': %w", service.Name, err)
		}

		if !healthCheckTimedOut.Load() {
			inspect, err := docker.ContainerExecInspect(ctx, exec.ID)
			if err != nil {
				cancel()
				return fmt.Errorf("failed to inspect docker exec for service '%s': %w", service.Name, err)
			}
			if inspect.ExitCode == 0 {
				healthCheckLog.Debug("Service is ready")
				cancel()
				return nil
			}

			healthCheckLog.Debug("Service health check unsuccessful, retrying...", "exitcode", inspect.ExitCode)
		} else {
			healthCheckLog.Debug("Service health check timed out, retrying...")
		}

		// Cancel the context for this iteration before the next retry,
		// instead of deferring to function return (which would accumulate).
		cancel()
	}

	return fmt.Errorf("failed health check for service '%s'", service.Name)
}
