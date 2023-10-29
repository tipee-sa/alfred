package flags

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/samber/lo"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	LogFormat                   = "log-format"
	LogLevel                    = "log-level"
	LogSource                   = "log-source"
	Port                        = "port"
	Provisioner                 = "provisioner"
	ProvisioningFailureCooldown = "provisioning-failure-cooldown"

	OpenstackMaxNodes        = "openstack-max-nodes"
	OpenstackMaxTasksPerNode = "openstack-max-tasks-per-node"
	OpenstackImage           = "openstack-image"
	OpenstackFlavor          = "openstack-flavor"
	OpenstackNetworks        = "openstack-networks"
	OpenstackSecurityGroups  = "openstack-security-groups"
	OpenstackSshUsername     = "openstack-ssh-username"
	OpenstackDockerHost      = "openstack-docker-host"
)

func init() {
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	// Alfred
	flags.String(LogFormat, "json", "log format (json, text)")
	flags.String(LogLevel, "INFO", "minimum log level")
	flags.Bool(LogSource, false, "add source code location to logs")
	flags.Int(Port, 25373, "listening port")
	flags.String(Provisioner, "local", "node provisioner to use (local, openstack)")
	flags.Duration(ProvisioningFailureCooldown, 1*time.Minute, "how long to wait before retrying provisioning")

	// Openstack
	flags.Int(OpenstackMaxNodes, 1, "maximum number of nodes to provision")
	flags.Int(OpenstackMaxTasksPerNode, 2, "maximum number of tasks to run on a single node")
	flags.String(OpenstackImage, "", "image to use for provisioning")
	flags.String(OpenstackFlavor, "", "flavor to use for provisioning")
	flags.StringSlice(OpenstackNetworks, nil, "networks attached to the nodes")
	flags.StringSlice(OpenstackSecurityGroups, nil, "security groups defined for the nodes")
	flags.String(OpenstackSshUsername, "", "ssh username used to connect to the nodes")
	flags.String(OpenstackDockerHost, "", "docker host on the nodes")

	// Init
	if err := flags.Parse(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}

	viper.SetEnvPrefix("alfred")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	lo.Must0(viper.BindPFlags(flags))
}
