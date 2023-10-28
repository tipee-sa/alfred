package main

import (
	"fmt"
	"github.com/gammadia/alfred/provisioner/local"
	"github.com/gammadia/alfred/provisioner/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"os"
	"os/signal"
	"time"

	"github.com/gammadia/alfred/scheduler"
)

func main() {
	var provisionerID = os.Getenv("PROVISIONER")
	var provisioner scheduler.Provisioner
	var err error

	switch provisionerID {
	case "local":
		provisioner, err = local.NewProvisioner()
	case "openstack":
		provisioner, err = openstack.NewProvisioner(openstack.Config{
			MaxNodes:        1,
			MaxTasksPerNode: 2,

			Image:  "14841daa-5d0e-4445-8064-8e39e49558f1", // alfred-node-template-20231027142448
			Flavor: "21aad244-a330-4e79-ba80-4c057cf742f9", // a1-ram2-disk20-perf1
			Networks: []servers.Network{
				{UUID: "dcf25c41-9057-4bc2-8475-a2e3c5d8c662"}, // ext-net-1
			},
			SecurityGroups: []string{"alfred-node"},
			Username:       "debian",
			DockerHost:     "tcp://127.0.0.1:2375",
		})
	default:
		provisioner, err = nil, fmt.Errorf("unknown provisioner '%s'", provisionerID)
	}
	if err != nil {
		fmt.Printf(fmt.Errorf("unable to create provisioner '%s': %w\n", provisionerID, err).Error())
		os.Exit(1)
	}

	sched := scheduler.NewScheduler(provisioner, scheduler.Config{
		ProvisioningFailureCooldown: 10 * time.Second,
	})

	job := scheduler.Job{
		Image: "alpine:latest",
		Tasks: []string{"clairbois", "tipee", "gammadia", "alfred", "golang", "playground"},
		Script: `
		#!/bin/sh
		echo "Hello $TASK_NAME!"
		`,
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	go func() {
		<-sig
		sched.Shutdown()
		<-sig
		os.Exit(1)
	}()

	sched.Schedule(&job)
	sched.Wait()
}
