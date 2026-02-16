package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"github.com/gammadia/alfred/server/flags"
	"github.com/gammadia/alfred/server/log"

	"github.com/samber/lo"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Versioning information set at build time
var version, commit = "dev", "n/a"

var dataRoot string

// Global context for shutdown cascading. When cancel() is called (from signal handler),
// all goroutines watching ctx.Done() begin their shutdown sequence.
var ctx, cancel = context.WithCancel(context.Background())

// wg tracks the two main goroutines: scheduler and gRPC server.
// main() blocks on wg.Wait() and only exits when both are done.
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

	// Cleanup old artifacts
	if retention := viper.GetDuration(flags.ArtifactRetention); retention > 0 {
		cleanupOldArtifacts(dataRoot, retention)
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
	s := grpc.NewServer(grpc.MaxRecvMsgSize(config.MaxPacketSize))
	proto.RegisterAlfredServer(s, &server{})

	// Setup scheduler
	if err = createScheduler(); err != nil {
		log.Error("Failed to create scheduler", "error", err)
		os.Exit(1)
	}

	// Scheduler goroutine: Run() blocks in its event loop until Shutdown() is called.
	// A companion goroutine waits for ctx cancellation, then orchestrates a graceful
	// shutdown: Shutdown() signals the scheduler to stop, Wait() blocks until all
	// running tasks complete, then wg.Done() unblocks main.
	wg.Add(1)
	go scheduler.Run()
	go func() {
		<-ctx.Done()         // triggered by cancel() in signal handler
		scheduler.Shutdown() // closes scheduler's stop channel → Run() returns
		log.Debug("Waiting for scheduler to finish")
		scheduler.Wait() // blocks until all tasks finish (wg inside scheduler)
		log.Debug("Scheduler finished")
		wg.Done()
	}()

	// listenEvents runs in its own goroutine, consuming scheduler events to:
	// 1. Reconstruct serverStatus (the in-memory state used by all gRPC handlers)
	// 2. Forward filtered events to connected client watchers
	// It exits when the scheduler's event channel is closed (during Shutdown).
	channel, unsubscribe := scheduler.Subscribe()
	defer unsubscribe()
	go listenEvents(channel)

	// gRPC server goroutine. A nested goroutine watches for shutdown and calls
	// GracefulStop() with a timeout. GracefulStop stops accepting new connections and
	// waits for in-flight RPCs to complete, but streaming handlers (WatchJob, WatchStatus)
	// don't detect server shutdown via srv.Context() — they only exit on client disconnect.
	// If GracefulStop doesn't complete within 5 seconds, Stop() forces immediate closure.
	wg.Add(1)
	go func() {
		go func() {
			<-ctx.Done() // triggered by cancel() in signal handler
			graceful := make(chan struct{})
			go func() {
				s.GracefulStop()
				close(graceful)
			}()
			select {
			case <-graceful:
				log.Info("gRPC server stopped gracefully")
			case <-time.After(5 * time.Second):
				log.Warn("gRPC graceful stop timed out, forcing stop")
				s.Stop()
			}
		}()

		log.Info("Server listening", "address", lis.Addr())
		if err := s.Serve(lis); err != nil {
			log.Error("Failed to serve", "error", err)
			os.Exit(1)
		}
		wg.Done()
	}()

	// Block until both scheduler and gRPC server goroutines have finished.
	wg.Wait()
	log.Info("Shutdown completed. Bye!")
}

// setupInterrupts handles Ctrl+C (SIGINT) with a double-tap pattern:
// - First signal: calls cancel() which cascades shutdown through ctx.Done() to all goroutines
// - Second signal: forces immediate exit (in case graceful shutdown hangs)
func setupInterrupts() {
	sig := make(chan os.Signal, 1) // buffered: won't miss a signal while processing
	signal.Notify(sig, os.Interrupt)

	go func() {
		<-sig
		log.Info("Shutdown signal received, attempting graceful shutdown")
		cancel() // triggers ctx.Done() everywhere
		<-sig
		log.Warn("Second shutdown signal received, forcing exit")
		os.Exit(1)
	}()
}
