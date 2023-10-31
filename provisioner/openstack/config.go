package openstack

import (
	"log/slog"

	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
)

type Config struct {
	// Logger to use
	Logger *slog.Logger `json:"-"`
	// Image to use for nodes
	Image string `json:"openstack-image"`
	// Machine flavor to use for nodes
	Flavor string `json:"openstack-flavor"`
	// Networks to attach to nodes
	Networks []servers.Network `json:"openstack-network,omitempty"`
	// Security groups to attach to nodes
	SecurityGroups []string `json:"openstack-security-groups,omitempty"`
	// Username to use when connecting to nodes
	SshUsername string `json:"openstack-ssh-username"`
	// Docker host to use when connecting to nodes
	DockerHost string `json:"openstack-docker-host"`
	// Path to workspace
	Workspace string `json:"workspace"`
}
