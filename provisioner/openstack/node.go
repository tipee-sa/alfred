package openstack

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
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

func (n *Node) connect(server *servers.Server) (err error) {
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

	pages, err := servers.ListAddresses(openstackClient, server.ID).AllPages()
	if err != nil {
		return fmt.Errorf("failed to get server addresses for '%s': %w", n.name, err)
	}

	allAddresses, err := servers.ExtractAddresses(pages)
	if err != nil {
		return fmt.Errorf("failed to extract server addresses for '%s': %w", n.name, err)
	}

	var nodeAddress string
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

	initialWait, cmdTimeout, retryInterval, timeout := 5*time.Second, 5*time.Second, 2*time.Second, 1*time.Minute

	// Initialize SSH connection
	ctx, cancel := context.WithTimeout(context.TODO(), timeout)
	defer cancel()

	n.log.Debug("Wait for SSH daemon to start", "wait", initialWait)
	time.Sleep(initialWait)

	connectionAttempts := 1
	for n.ssh == nil {
		select {
		case <-ctx.Done():
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
				time.Sleep(retryInterval)
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
			if _, _, err := n.ssh.SendRequest("keepalive@alfred", true, nil); err != nil {
				n.log.Warn("SSH keepalive failed", "error", err)
				return
			}
		}
	}()

	n.log.Debug("Wait for Docker daemon to start", "wait", initialWait)
	time.Sleep(initialWait)

	// Initialize Docker client
	connectionAttempts = 1
	n.docker, err = client.NewClientWithOpts(
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
	for _, image := range task.Job.Steps {
		if err := n.ensureNodeHasImage(image); err != nil {
			return -1, fmt.Errorf("failed to ensure node has docker image '%s': %w", image, err)
		}
	}

	return internal.RunContainer(ctx, n.docker, task, n.fs, runConfig)
}

func (n *Node) ensureNodeHasImage(image string) error {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	n.log.Debug("Ensuring node has image", "image", image)

	if _, _, err := n.docker.ImageInspectWithRaw(context.Background(), image); err == nil {
		n.log.Debug("Image already on node", "image", image)
		return nil
	} else if !client.IsErrNotFound(err) {
		return fmt.Errorf("failed to inspect docker image '%s': %w", image, err)
	}

	n.log.Info("Image not on node, loading", "image", image)

	session, err := n.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	saveCmd := exec.Command("bash", "-euo", "pipefail", "-c", fmt.Sprintf("docker save '%s' | zstd --compress --adapt=min=5,max=8", image))
	saveOut := lo.Must(saveCmd.StdoutPipe())
	session.Stdin = saveOut
	session.Stderr = os.Stderr

	if err := saveCmd.Start(); err != nil {
		return fmt.Errorf("failed to 'docker save' image '%s': %w", image, err)
	}

	if err := session.Run("zstd --decompress | docker load"); err != nil {
		return fmt.Errorf("failed to 'docker load' image '%s': %w", image, err)
	}

	if err := saveCmd.Wait(); err != nil {
		return fmt.Errorf("failed while waiting for 'docker save' of image '%s': %w", image, err)
	}

	n.log.Debug("Image loaded on node", "image", image)
	return nil
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

	err := servers.Delete(n.provisioner.client, n.server.ID).ExtractErr()

	n.provisioner.wg.Done()
	return err
}
