package openstack

import "github.com/gophercloud/gophercloud/openstack/compute/v2/servers"

type Config struct {
	MaxNodes        int
	MaxTasksPerNode int

	Image          string
	Flavor         string
	Networks       []servers.Network
	SecurityGroups []string
	Username       string
	DockerHost     string
}
