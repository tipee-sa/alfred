package main

import (
	"context"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/server/log"
)

var docker *client.Client

func createDockerClient() (err error) {
	log.Debug("Creating docker client")
	if docker, err = client.NewClientWithOpts(); err != nil {
		return
	}
	if _, err = docker.Ping(context.Background()); err != nil {
		return
	}
	return
}
