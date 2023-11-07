package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/flags"
	"github.com/gammadia/alfred/server/log"

	"github.com/samber/lo"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	_ "github.com/mostynb/go-grpc-compression/zstd"
)

// Versioning information set at build time
var version, commit = "dev", "n/a"

var dataRoot string
var ctx, cancel = context.WithCancel(context.Background())
var wg sync.WaitGroup

func main() {
	// Setup logger first as this will be used to report progress of the rest of the setup
	if err := log.Init(); err != nil {
		lo.Must(fmt.Fprintln(os.Stderr, err))
		os.Exit(1)
	}
	log.Info("Alfred server starting up...", "version", version, "commit", commit)
	serverStatus.Server.StartedAt = timestamppb.Now()

	// Create data directory
	dataRoot = viper.GetString(flags.ServerData)
	if err := os.MkdirAll(dataRoot, 0755); err != nil {
		log.Error("Failed to create data directory", "error", err)
		os.Exit(1)
	}

	// Setup network listener
	lis, err := net.Listen("tcp", viper.GetString(flags.Listen))
	if err != nil {
		log.Error("Failed to listen", "error", err)
		os.Exit(1)
	}

	// Connect to the local Docker daemon
	if err = createDockerClient(); err != nil {
		log.Error("Failed to connect to Docker", "error", err)
		os.Exit(1)
	}

	// Setup signal handling for graceful shutdown
	setupInterrupts()

	// Setup gRPC server
	s := grpc.NewServer()
	proto.RegisterAlfredServer(s, &server{})

	// Setup scheduler
	if err = createScheduler(); err != nil {
		log.Error("Failed to create scheduler", "error", err)
		os.Exit(1)
	}

	wg.Add(1)
	go scheduler.Run()
	go func() {
		<-ctx.Done()
		scheduler.Shutdown()
		scheduler.Wait()
		wg.Done()
	}()

	// Listen for events from the scheduler
	channel, unsubscribe := scheduler.Subscribe()
	defer unsubscribe()
	go listenEvents(channel)

	// Start serving gRPC requests
	wg.Add(1)
	go func() {
		go func() {
			<-ctx.Done()
			s.GracefulStop()
		}()

		log.Info("Server listening", "address", lis.Addr())
		if err := s.Serve(lis); err != nil {
			log.Error("Failed to serve", "error", err)
			os.Exit(1)
		}
		wg.Done()
	}()

	// Wait for shutdown before finishing the main goroutine
	wg.Wait()
	log.Info("Shutdown completed. Bye!")
}

func setupInterrupts() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	go func() {
		<-sig
		log.Info("Shutdown signal received, attempting graceful shutdown")
		cancel()
		<-sig
		log.Warn("Second shutdown signal received, forcing exit")
		os.Exit(1)
	}()
}
