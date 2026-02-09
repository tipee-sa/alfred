package openstack

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gammadia/alfred/namegen"
	"github.com/gammadia/alfred/scheduler"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/schedulerhints"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/pagination"
	"golang.org/x/crypto/ssh"
)

type Provisioner struct {
	name   string
	config Config
	client *gophercloud.ServiceClient
	log    *slog.Logger
	fs     *fs

	keypairName string
	privateKey  ssh.Signer

	// flavorCache caches flavor name → UUID resolutions. Protected by flavorCacheMu.
	// Flavor names don't change during a session, so lookups are cached indefinitely.
	flavorCache   map[string]string
	flavorCacheMu sync.Mutex

	// WaitGroup tracking all provisioned resources
	// An item is added to track the first call to Shutdown()
	wg sync.WaitGroup

	// True if Shutdown() has been called
	shutdown atomic.Bool
}

// Provisioner implements scheduler.Provisioner
var _ scheduler.Provisioner = (*Provisioner)(nil)

func New(config Config) (*Provisioner, error) {
	// OpenStack authentication
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to get auth options from env: %w", err)
	}

	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get authenticated client: %w", err)
	}

	// OpenStack compute client
	client, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get compute client: %w", err)
	}

	// Provisioner object
	name := namegen.Get()
	provisioner := &Provisioner{
		name:   name,
		config: config,
		client: client,
		log:    config.Logger.With(slog.Group("provisioner", "name", name)),

		keypairName: fmt.Sprintf("alfred-%s", name),
		flavorCache: make(map[string]string),
	}
	provisioner.wg.Add(1) // One item for the provisioner itself

	// Generate a temporary keypair
	provisioner.log.Debug("Creating SSH keypair", "keypair", provisioner.keypairName)
	keypair, err := keypairs.Create(client, keypairs.CreateOpts{Name: provisioner.keypairName}).Extract()
	if err != nil {
		return nil, fmt.Errorf("failed to create keypair: %w", err)
	} else {
		provisioner.wg.Add(1) // One item for the keypair
		provisioner.privateKey, err = ssh.ParsePrivateKey([]byte(keypair.PrivateKey))
		if err != nil {
			provisioner.deleteKeypair()
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
	}

	return provisioner, nil
}

func (p *Provisioner) deleteKeypair() {
	p.log.Debug("Deleting SSH keypair", "keypair", p.keypairName)
	err := keypairs.Delete(p.client, p.keypairName, nil).ExtractErr()
	p.wg.Done()
	if err != nil {
		p.log.Warn("Failed to delete keypair", "error", err)
	}
}

func (p *Provisioner) GetName() string {
	return p.name
}

func (p *Provisioner) Provision(nodeName string, flavor string) (scheduler.Node, error) {
	flavorRef, err := p.resolveFlavor(flavor)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve flavor '%s': %w", flavor, err)
	}

	name := fmt.Sprintf("alfred-%s", nodeName)
	var optsBuilder servers.CreateOptsBuilder = keypairs.CreateOptsExt{
		CreateOptsBuilder: servers.CreateOpts{
			Name:           name,
			ImageRef:       p.config.Image,
			FlavorRef:      flavorRef,
			Networks:       p.config.Networks,
			SecurityGroups: p.config.SecurityGroups,
			Metadata: map[string]string{
				"alfred-provisioner":    p.name,
				"alfred-provisioned-at": time.Now().Format(time.RFC3339),
			},
		},
		KeyName: p.keypairName,
	}
	if p.config.ServerGroup != "" {
		optsBuilder = schedulerhints.CreateOptsExt{
			CreateOptsBuilder: optsBuilder,
			SchedulerHints: schedulerhints.SchedulerHints{
				Group: p.config.ServerGroup,
			},
		}
	}

	server, err := servers.Create(p.client, optsBuilder).Extract()
	if err != nil {
		return nil, fmt.Errorf("failed to create server '%s': %w", name, err)
	}

	node := &Node{
		name:        name,
		provisioner: p,
		server:      server,
		log:         p.log.With(slog.Group("node", "name", name)),
	}

	node.log.Info("Node created")
	return node, node.connect(server)
}

func (p *Provisioner) Shutdown() {
	if !p.shutdown.CompareAndSwap(false, true) {
		p.log.Debug("Ignoring duplicate call to Shutdown()")
		return
	}
	p.deleteKeypair()
	p.wg.Done() // Remove the provisioner itself from the WaitGroup
}

func (p *Provisioner) Wait() {
	p.wg.Wait()
}

var uuidRegexp = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// resolveFlavor resolves a flavor name to its UUID. If the input already looks
// like a UUID, it is returned as-is. Results are cached for the session lifetime.
func (p *Provisioner) resolveFlavor(flavor string) (string, error) {
	if uuidRegexp.MatchString(flavor) {
		return flavor, nil
	}

	p.flavorCacheMu.Lock()
	defer p.flavorCacheMu.Unlock()

	if id, ok := p.flavorCache[flavor]; ok {
		return id, nil
	}

	var flavorID string
	err := flavors.ListDetail(p.client, nil).EachPage(func(page pagination.Page) (bool, error) {
		flavorList, err := flavors.ExtractFlavors(page)
		if err != nil {
			return false, err
		}
		for _, f := range flavorList {
			if f.Name == flavor {
				flavorID = f.ID
				return false, nil // stop paging
			}
		}
		return true, nil // continue to next page
	})
	if err != nil {
		return "", fmt.Errorf("failed to list flavors: %w", err)
	}
	if flavorID == "" {
		return "", fmt.Errorf("flavor '%s' not found", flavor)
	}

	p.flavorCache[flavor] = flavorID
	return flavorID, nil
}
