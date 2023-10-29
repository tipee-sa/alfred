package openstack

import (
	"log/slog"

	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
)

type Config struct {
	// Logger to use
	Logger *slog.Logger
	// Maximum number of nodes that can be provisioned
	MaxNodes int
	// Maximum number of tasks that can be run on a single node
	MaxTasksPerNode int
	// Image to use for nodes
	Image string
	// Machine flavor to use for nodes
	Flavor string
	// Networks to attach to nodes
	Networks []servers.Network
	// Security groups to attach to nodes
	SecurityGroups []string
	// Username to use when connecting to nodes
	SshUsername string
	// Docker host to use when connecting to nodes
	DockerHost string
}
