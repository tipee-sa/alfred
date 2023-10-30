package openstack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
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

	terminated bool

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
	err = servers.WaitForStatus(openstackClient, server.ID, "ACTIVE", 60)
	if err != nil {
		return fmt.Errorf("failed while waiting for server '%s' to become ready: %w", n.name, err)
	}

	pages, err := servers.ListAddresses(openstackClient, server.ID).AllPages()
	if err != nil {
		return fmt.Errorf("failed to get server '%s' addresses: %w", n.name, err)
	}

	allAddresses, err := servers.ExtractAddresses(pages)
	if err != nil {
		return fmt.Errorf("failed to extract server '%s' addresses: %w", n.name, err)
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

	// Initialize SSH connection
	ctx, cancel := context.WithTimeout(context.TODO(), 1*time.Minute)
	defer cancel()

	for n.ssh == nil {
		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to connect to server '%s' after 1 minute", n.name)

		default:
			time.Sleep(5 * time.Second)
			n.ssh, err = ssh.Dial("tcp", fmt.Sprintf("%s:22", nodeAddress), &ssh.ClientConfig{
				User:            n.provisioner.config.SshUsername,
				Timeout:         10 * time.Second,
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Auth: []ssh.AuthMethod{
					ssh.PublicKeys(n.provisioner.privateKey),
				},
			})
			if err != nil {
				switch {
				case errors.Is(err, syscall.ECONNREFUSED),
					errors.Is(err, syscall.ETIMEDOUT),
					errors.Is(err, os.ErrDeadlineExceeded):
					n.log.Debug("SSH connection to server refused, retrying in 5 seconds")

				default:
					return fmt.Errorf("failed to connect to server '%s': %w", n.name, err)
				}
			}
		}
	}

	// Initialize Docker client
	n.docker, err = client.NewClientWithOpts(
		client.WithHost(n.provisioner.config.DockerHost),
		client.WithDialContext(func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
			ctx, cancel := context.WithDeadline(ctx, time.Now().Add(2*time.Minute))
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("failed to dial docker: %w", ctx.Err())

				default:
					if conn, err = n.ssh.Dial(network, addr); err == nil {
						return
					} else {
						n.log.Debug("Connection to Docker daemon refused, retrying in 5 seconds")
						time.Sleep(5 * time.Second)
					}
				}
			}
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to init docker client: %w", err)
	}

	return nil
}

func (n *Node) Run(task *scheduler.Task) error {
	if err := n.ensureNodeHasImage(task.Job.Image); err != nil {
		return fmt.Errorf("node has image: %w", err)
	}

	return internal.RunContainer(context.TODO(), n.docker, task)
}

func (n *Node) ensureNodeHasImage(image string) error {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	n.log.Debug("Ensuring node has image", "image", image)

	if _, _, err := n.docker.ImageInspectWithRaw(context.Background(), image); err == nil {
		n.log.Debug("Image already on node", "image", image)
		return nil
	} else if !client.IsErrNotFound(err) {
		return fmt.Errorf("inspect image: %w", err)
	}

	n.log.Info("Image not on node, loading", "image", image)

	session, err := n.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	saveCmd := exec.Command("docker", "save", image)
	saveOut := lo.Must(saveCmd.StdoutPipe())
	session.Stdin = saveOut

	if err := saveCmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker save: %w", err)
	}

	if err := session.Run("docker load"); err != nil {
		return fmt.Errorf("failed docker load: %w", err)
	}

	if err := saveCmd.Wait(); err != nil {
		return fmt.Errorf("failed docker save: %w", err)
	}

	n.log.Debug("Image loaded on node", "image", image)
	return nil
}

func (n *Node) Terminate() error {
	if n.terminated {
		return fmt.Errorf("node '%s' already terminated", n.name)
	}
	n.terminated = true

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
