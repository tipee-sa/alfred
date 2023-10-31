package main

import (
	"encoding/json"
	"fmt"

	"github.com/gammadia/alfred/provisioner/local"
	"github.com/gammadia/alfred/provisioner/openstack"
	s "github.com/gammadia/alfred/scheduler"
	"github.com/gammadia/alfred/server/flags"
	"github.com/gammadia/alfred/server/log"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/samber/lo"
	"github.com/spf13/viper"
)

var scheduler *s.Scheduler

func createScheduler() error {
	provisioner, err := createProvisioner()
	if err != nil {
		return fmt.Errorf("unable to create provisioner '%s': %w", viper.GetString(flags.Provisioner), err)
	}

	scheduler = s.New(provisioner, s.Config{
		Logger:                      log.Base.With("component", "scheduler"),
		MaxNodes:                    viper.GetInt(flags.ProvisionerMaxNodes),
		MaxTasksPerNode:             viper.GetInt(flags.ProvisionerMaxTasksPerNode),
		ProvisioningDelay:           viper.GetDuration(flags.ProvisioningDelay),
		ProvisioningFailureCooldown: viper.GetDuration(flags.ProvisioningFailureCooldown),
	})

	return nil
}

func createProvisioner() (s.Provisioner, error) {
	logger := log.Base.With("component", "provisioner")
	switch p := viper.GetString(flags.Provisioner); p {
	case "local":
		config := local.Config{
			Logger: logger,
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
		}
		logger.Debug("Provisioner config", "provisioner", p, "config", string(lo.Must(json.Marshal(config))))
		return openstack.New(config)
	default:
		return nil, fmt.Errorf("unknown provisioner")
	}
}
