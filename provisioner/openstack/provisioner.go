package openstack

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/gammadia/alfred/namegen"
	"github.com/gammadia/alfred/scheduler"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"golang.org/x/crypto/ssh"
)

type Provisioner struct {
	name   namegen.ID
	config Config
	client *gophercloud.ServiceClient
	logger *log.Logger

	keyName    string
	privateKey ssh.Signer

	wg sync.WaitGroup
}

// Provisioner implements scheduler.Provisioner
var _ scheduler.Provisioner = (*Provisioner)(nil)

func NewProvisioner(config Config) (*Provisioner, error) {
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to get auth options from env: %w", err)
	}

	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get authenticated client: %w", err)
	}

	client, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get compute client: %w", err)
	}

	name := namegen.Get()
	provisioner := &Provisioner{
		name:   name,
		config: config,
		client: client,

		keyName: fmt.Sprintf("alfred-%s", name),
	}

	keypair, err := keypairs.Create(client, keypairs.CreateOpts{Name: provisioner.keyName}).Extract()
	if err != nil {
		return nil, fmt.Errorf("failed to create keypair: %w", err)
	} else {
		provisioner.privateKey, err = ssh.ParsePrivateKey([]byte(keypair.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
	}
	provisioner.wg.Add(1) // One item for the keypair

	return provisioner, nil
}

func (p *Provisioner) SetLogger(logger *log.Logger) {
	p.logger = logger
}

func (p *Provisioner) MaxNodes() int {
	return p.config.MaxNodes
}

func (p *Provisioner) MaxTasksPerNode() int {
	return p.config.MaxTasksPerNode
}

func (p *Provisioner) Provision() (scheduler.Node, error) {
	name := fmt.Sprintf("alfred-%s", namegen.Get())

	server, err := servers.Create(p.client, keypairs.CreateOptsExt{
		CreateOptsBuilder: servers.CreateOpts{
			Name:           name,
			ImageRef:       p.config.Image,
			FlavorRef:      p.config.Flavor,
			Networks:       p.config.Networks,
			SecurityGroups: p.config.SecurityGroups,
			Metadata: map[string]string{
				"alfred-provisioner":    p.name.String(),
				"alfred-provisioned-at": time.Now().Format(time.RFC3339),
			},
		},
		KeyName: p.keyName,
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("failed to create server '%s': %w", name, err)
	}

	node := &Node{
		name:        name,
		provisioner: p,
		server:      server,
	}

	node.provisioner.logger.Printf("Created server '%s', waiting for it to become ready", name)
	return node, node.connect(server)
}

func (p *Provisioner) Shutdown() {
	err := keypairs.Delete(p.client, p.keyName, nil).ExtractErr()
	if err != nil {
		p.logger.Printf("Failed to delete keypair '%s': %v", p.keyName, err)
	}
	p.wg.Done()
}

func (p *Provisioner) Wait() {
	p.wg.Wait()
}
