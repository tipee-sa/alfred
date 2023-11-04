package flags

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/samber/lo"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	Listen                      = "listen"
	LogFormat                   = "log-format"
	LogLevel                    = "log-level"
	LogSource                   = "log-source"
	MaxNodes                    = "max-nodes"
	NodeWorkspace               = "node-workspace"
	Provisioner                 = "provisioner"
	ProvisioningDelay           = "provisioning-delay"
	ProvisioningFailureCooldown = "provisioning-failure-cooldown"
	ServerData                  = "server-data"
	TasksPerNode                = "tasks-per-node"

	OpenstackDockerHost     = "openstack-docker-host"
	OpenstackFlavor         = "openstack-flavor"
	OpenstackImage          = "openstack-image"
	OpenstackNetworks       = "openstack-networks"
	OpenstackSecurityGroups = "openstack-security-groups"
	OpenstackSshUsername    = "openstack-ssh-username"
)

func init() {
	workDir := lo.Must(os.Getwd())
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	// Alfred
	flags.String(LogFormat, "json", "log format (json, text)")
	flags.String(LogLevel, "INFO", "minimum log level")
	flags.Bool(LogSource, false, "add source code location to logs")
	flags.Int(MaxNodes, (runtime.NumCPU()+1)/2, "maximum number of nodes to provision")
	flags.String(NodeWorkspace, path.Join(workDir, "var", "node-workspace"), "workspace directory for nodes")
	flags.String(Listen, ":25373", "listening address and port")
	flags.String(Provisioner, "local", "node provisioner to use (local, openstack)")
	flags.Duration(ProvisioningDelay, 20*time.Second, "how long to wait between provisioning nodes")
	flags.Duration(ProvisioningFailureCooldown, 1*time.Minute, "how long to wait before retrying provisioning")
	flags.String(ServerData, path.Join(workDir, "var", "server-data"), "data directory for server")
	flags.Int(TasksPerNode, 2, "maximum number of tasks to run on a single node")

	// Openstack
	flags.String(OpenstackDockerHost, "", "docker host on the nodes")
	flags.String(OpenstackFlavor, "", "flavor to use for provisioning")
	flags.String(OpenstackImage, "", "image to use for provisioning")
	flags.StringSlice(OpenstackNetworks, nil, "networks attached to the nodes")
	flags.StringSlice(OpenstackSecurityGroups, nil, "security groups defined for the nodes")
	flags.String(OpenstackSshUsername, "", "ssh username used to connect to the nodes")

	// Init
	if err := flags.Parse(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			lo.Must(fmt.Fprintln(os.Stderr, err))
		}
		os.Exit(1)
	}

	viper.SetEnvPrefix("alfred")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	lo.Must0(viper.BindPFlags(flags))
}
