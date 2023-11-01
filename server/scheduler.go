package main

import (
	"encoding/json"
	"fmt"

	"github.com/gammadia/alfred/provisioner/local"
	"github.com/gammadia/alfred/provisioner/openstack"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"github.com/gammadia/alfred/server/flags"
	"github.com/gammadia/alfred/server/log"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/samber/lo"
	"github.com/spf13/viper"
)

var scheduler *schedulerpkg.Scheduler

func createScheduler() error {
	provisioner, err := createProvisioner()
	if err != nil {
		return fmt.Errorf("unable to create provisioner '%s': %w", viper.GetString(flags.Provisioner), err)
	}

	scheduler = schedulerpkg.New(provisioner, schedulerpkg.Config{
		Logger:                      log.Base.With("component", "scheduler"),
		MaxNodes:                    viper.GetInt(flags.MaxNodes),
		ProvisioningDelay:           viper.GetDuration(flags.ProvisioningDelay),
		ProvisioningFailureCooldown: viper.GetDuration(flags.ProvisioningFailureCooldown),
		TasksPerNode:                viper.GetInt(flags.TasksPerNode),
	})

	serverStatus.Scheduler.Provisioner = viper.GetString(flags.Provisioner)
	serverStatus.Scheduler.MaxNodes = uint32(viper.GetInt(flags.MaxNodes))
	serverStatus.Scheduler.TasksPerNodes = uint32(viper.GetDuration(flags.TasksPerNode))

	return nil
}

func createProvisioner() (schedulerpkg.Provisioner, error) {
	logger := log.Base.With("component", "provisioner")
	switch p := viper.GetString(flags.Provisioner); p {
	case "local":
		config := local.Config{
			Logger:    logger,
			Workspace: viper.GetString(flags.NodeWorkspace),
		}
		logger.Debug("Provisioner config", "provisioner", p, "config", string(lo.Must(json.Marshal(config))))
		return local.New(config)

	case "openstack":
		config := openstack.Config{
			Logger: logger,
			Image:  viper.GetString(flags.OpenstackImage),
			Flavor: viper.GetString(flags.OpenstackFlavor),
			Networks: lo.Map(
				viper.GetStringSlice(flags.OpenstackNetworks),
				func(s string, _ int) servers.Network {
					return servers.Network{UUID: s}
				},
			),
			SecurityGroups: viper.GetStringSlice(flags.OpenstackSecurityGroups),
			SshUsername:    viper.GetString(flags.OpenstackSshUsername),
			DockerHost:     viper.GetString(flags.OpenstackDockerHost),
			Workspace:      viper.GetString(flags.NodeWorkspace),
		}
		logger.Debug("Provisioner config", "provisioner", p, "config", string(lo.Must(json.Marshal(config))))
		return openstack.New(config)
	default:
		return nil, fmt.Errorf("unknown provisioner")
	}
}
