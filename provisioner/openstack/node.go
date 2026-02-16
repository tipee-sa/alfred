package openstack

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/provisioner/internal"
	"github.com/gammadia/alfred/scheduler"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/samber/lo"
	"golang.org/x/crypto/ssh"
)

type Node struct {
	name        string
	provisioner *Provisioner
	server      *servers.Server
	ssh         *ssh.Client
	docker      *client.Client
	fs          *fs

	terminated atomic.Bool

	log   *slog.Logger
	mutex sync.Mutex
}

// Node implements scheduler.Node
var _ scheduler.Node = (*Node)(nil)

func (n *Node) Name() string {
	return n.name
}

func (n *Node) connect(ctx context.Context, server *servers.Server) (err error) {
	n.provisioner.wg.Add(1)
	defer func() {
		if err != nil {
			_ = n.Terminate()
		}
	}()

	openstackClient := n.provisioner.client

	n.log.Debug("Wait for server to become ready", "wait", 2*time.Minute)
	err = servers.WaitForStatus(openstackClient, server.ID, "ACTIVE", 120)
	if err != nil {
		return fmt.Errorf("failed while waiting for server '%s' to become ready after %s: %w", n.name, 2*time.Minute, err)
	}

	var nodeAddress string
	if err := internal.Retry(4, func() error {
		pages, err := servers.ListAddresses(openstackClient, server.ID).AllPages()
		if err != nil {
			return fmt.Errorf("failed to get server addresses for '%s': %w", n.name, err)
		}

		allAddresses, err := servers.ExtractAddresses(pages)
		if err != nil {
			return fmt.Errorf("failed to extract server addresses for '%s': %w", n.name, err)
		}

		nodeAddress = ""
		for _, addresses := range allAddresses {
			for _, address := range addresses {
				if address.Version == 4 {
					nodeAddress = address.Address
				}
			}
		}
		if nodeAddress == "" {
			return fmt.Errorf("failed to find IPv4 address for server '%s'", n.name)
		}
		return nil
	}); err != nil {
		return err
	}

	initialWait, cmdTimeout, retryInterval, timeout := 5*time.Second, 5*time.Second, 2*time.Second, 1*time.Minute

	// Initialize SSH connection
	sshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	n.log.Debug("Wait for SSH daemon to start", "wait", initialWait)
	select {
	case <-time.After(initialWait):
	case <-ctx.Done():
		return ctx.Err()
	}

	connectionAttempts := 1
	for n.ssh == nil {
		select {
		case <-sshCtx.Done():
			return fmt.Errorf("failed to connect to server '%s' after %s and %d attempts: %w", n.name, timeout, connectionAttempts, err)

		default:
			n.ssh, err = ssh.Dial("tcp", fmt.Sprintf("%s:22", nodeAddress), &ssh.ClientConfig{
				User:            n.provisioner.config.SshUsername,
				Timeout:         cmdTimeout,
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Auth: []ssh.AuthMethod{
					ssh.PublicKeys(n.provisioner.privateKey),
				},
			})
			if err != nil {
				n.log.Debug(fmt.Errorf("Connection to node refused (attempt %d), retrying in %s: %w", connectionAttempts, retryInterval, err).Error())
				select {
				case <-time.After(retryInterval):
				case <-sshCtx.Done():
					return fmt.Errorf("failed to connect to server '%s' after %s and %d attempts: %w", n.name, timeout, connectionAttempts, err)
				}
				connectionAttempts += 1
			}
		}
	}

	// Start SSH keepalive goroutine to prevent connection from dying during long tasks (4h+)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if n.terminated.Load() {
				return
			}
			if err := internal.Retry(3, func() error {
				_, _, err := n.ssh.SendRequest("keepalive@alfred", true, nil)
				return err
			}); err != nil {
				n.log.Warn("SSH keepalive failed after retries", "error", err)
				return
			}
		}
	}()

	n.log.Debug("Wait for Docker daemon to start", "wait", initialWait)
	select {
	case <-time.After(initialWait):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Initialize Docker client
	connectionAttempts = 1
	n.docker, err = client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
		client.WithHost(n.provisioner.config.DockerHost),
		client.WithDialContext(func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("failed to connect to Docker daemon after %s and %d attempts: %w", timeout, connectionAttempts, ctx.Err())

				default:
					if conn, err = n.ssh.Dial(network, addr); err == nil {
						return
					} else {
						n.log.Debug(fmt.Errorf("Connection to Docker daemon refused (attempt %d), retrying in %s: %w", connectionAttempts, retryInterval, err).Error())
						time.Sleep(retryInterval)
						connectionAttempts += 1
					}
				}
			}
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize docker client: %w", err)
	}

	// Initialize file system
	n.fs = newFs(n.provisioner.config.Workspace, n.ssh)

	return nil
}

func (n *Node) RunTask(ctx context.Context, task *scheduler.Task, runConfig scheduler.RunTaskConfig) (int, error) {
	// Resolve each step image to a tagged reference for this node.
	// Containerd image store requires tagged references — images loaded via docker load
	// are not accessible by their config digest (SHA).
	imageOverrides := map[string]string{}
	for i, image := range task.Job.Steps {
		n.log.Debug("Resolving step image", "step", i, "image", image)
		ref, err := n.ensureNodeHasImage(image)
		if err != nil {
			return -1, fmt.Errorf("failed to ensure node has docker image '%s': %w", image, err)
		}
		if ref != image {
			n.log.Debug("Step image resolved to different reference", "step", i, "original", image, "resolved", ref)
			imageOverrides[image] = ref
		}
	}

	if len(imageOverrides) > 0 {
		n.log.Debug("Running container with image overrides", "overrides", imageOverrides)
	}

	return internal.RunContainer(ctx, n.docker, task, n.fs, runConfig, imageOverrides)
}

func (n *Node) ensureNodeHasImage(image string) (string, error) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	tag := fmt.Sprintf("alfred-transfer:%s", strings.TrimPrefix(image, "sha256:"))
	n.log.Debug("Ensuring node has image", "image", image, "tag", tag)

	// Check if image was already transferred (from a previous task on this node)
	if err := internal.Retry(3, func() error {
		_, _, err := n.docker.ImageInspectWithRaw(context.Background(), tag)
		return err
	}); err == nil {
		n.log.Debug("Image found on node (from previous transfer)", "image", image, "tag", tag)
		return tag, nil
	} else {
		n.log.Debug("Image not found on node", "image", image, "tag", tag)
	}

	n.log.Info("Image not on node, loading", "image", image, "tag", tag)

	// Tag the image on the server with a predictable name before saving, so that docker load
	// on the node registers it with a tag. Containerd image store does not make loaded images
	// accessible by their config digest (image ID), so a tag is required.
	n.log.Debug("Tagging image on server", "image", image, "tag", tag)
	tagCmd := exec.Command("docker", "tag", image, tag)
	if out, err := tagCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to tag image '%s' as '%s' on server (output: %s): %w", image, tag, strings.TrimSpace(string(out)), err)
	}
	n.log.Debug("Tagged image on server", "image", image, "tag", tag)

	// Transfer the image to the node via docker save | zstd | ssh | docker load.
	// Retry the entire pipeline on failure (SSH tunnel drops, Docker daemon busy, etc.).
	const maxTransferAttempts = 3
	var lastTransferErr error
	var transferAttempts int
	for transferAttempts = 1; transferAttempts <= maxTransferAttempts; transferAttempts++ {
		if transferAttempts > 1 {
			n.log.Warn("Image transfer failed, retrying", "image", image, "tag", tag, "attempt", transferAttempts, "maxAttempts", maxTransferAttempts, "error", lastTransferErr)
			time.Sleep(5 * time.Second)
		}

		lastTransferErr = func() error {
			session, err := newSession(n.ssh)
			if err != nil {
				return err
			}
			defer session.Close()

			n.log.Debug("Starting docker save|zstd|ssh|docker load pipeline", "tag", tag, "attempt", transferAttempts)
			saveCmd := exec.Command("bash", "-euo", "pipefail", "-c", fmt.Sprintf("docker save '%s' | zstd --compress --adapt=min=5,max=8", tag))
			saveOut := lo.Must(saveCmd.StdoutPipe())
			session.Stdin = saveOut
			var loadOutput bytes.Buffer
			session.Stdout = &loadOutput
			session.Stderr = os.Stderr

			if err := saveCmd.Start(); err != nil {
				return fmt.Errorf("failed to 'docker save' image '%s': %w", image, err)
			}

			if err := session.Run("zstd --decompress | docker load"); err != nil {
				// Ensure the local save process is cleaned up
				saveCmd.Wait()
				return fmt.Errorf("failed to 'docker load' image '%s' (docker load output: %s): %w",
					image, strings.TrimSpace(loadOutput.String()), err)
			}

			if err := saveCmd.Wait(); err != nil {
				return fmt.Errorf("failed while waiting for 'docker save' of image '%s': %w", image, err)
			}

			return nil
		}()

		if lastTransferErr == nil {
			break
		}
	}
	if lastTransferErr != nil {
		return "", fmt.Errorf("image transfer failed after %d attempts: %w", maxTransferAttempts, lastTransferErr)
	}

	n.log.Debug("Docker save|load pipeline completed", "image", image, "tag", tag, "attempts", transferAttempts)

	// Verify the image is accessible by tag
	if err := internal.Retry(3, func() error {
		_, _, err := n.docker.ImageInspectWithRaw(context.Background(), tag)
		return err
	}); err == nil {
		n.log.Debug("Image accessible by tag after loading", "image", image, "tag", tag)
		return tag, nil
	} else {
		n.log.Debug("Image NOT accessible by tag after loading", "tag", tag, "error", err)
	}

	// Last resort: list all images on the node to help diagnose
	n.log.Warn("Image not accessible by tag after docker load, listing all images on node for diagnostics",
		"image", image, "tag", tag)
	diagSession, diagErr := newSession(n.ssh)
	if diagErr == nil {
		var diagOutput bytes.Buffer
		diagSession.Stdout = &diagOutput
		if diagSession.Run("docker images --no-trunc --format '{{.ID}} {{.Repository}}:{{.Tag}}'") == nil {
			n.log.Warn("Images on node", "images", strings.TrimSpace(diagOutput.String()))
		}
		diagSession.Close()
	}

	return "", fmt.Errorf("image '%s' not accessible by tag '%s' after docker load",
		image, tag)
}

func (n *Node) Terminate() error {
	if !n.terminated.CompareAndSwap(false, true) {
		return nil
	}

	if n.ssh != nil {
		_ = n.ssh.Close()
	}

	if n.docker != nil {
		_ = n.docker.Close()
	}

	err := internal.Retry(3, func() error {
		return servers.Delete(n.provisioner.client, n.server.ID).ExtractErr()
	})

	n.provisioner.wg.Done()
	return err
}
