package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"

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
		ArtifactPreserver:           preserveArtifacts,
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

func preserveArtifacts(reader io.Reader, task *schedulerpkg.Task) error {
	artifactsDir := path.Join(dataRoot, "artifacts", task.Job.FQN())
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return fmt.Errorf("failed to create artifacts directory: %w", err)
	}

	artifactFile := fmt.Sprintf("%s.tar.gz", task.Name)
	file, err := os.OpenFile(path.Join(artifactsDir, artifactFile), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create artifact file: %w", err)
	}

	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("failed to copy artifact file: %w", err)
	}

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
