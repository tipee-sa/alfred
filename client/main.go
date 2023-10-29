package main

import (
	"fmt"
	"os"

	dockerclient "github.com/docker/docker/client"
	"github.com/fatih/color"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding/gzip"
)

// Versioning information set at build time
var version, commit = "dev", "n/a"

var clientConn *grpc.ClientConn
var client proto.AlfredClient

var docker = lo.Must(dockerclient.NewClientWithOpts())

var verbose bool

var alfredCmd = &cobra.Command{
	Use:   "alfred",
	Short: "Alfred is a distributed job scheduler.",

	SilenceUsage:  true,
	SilenceErrors: true,

	PersistentPreRunE: func(cmd *cobra.Command, args []string) (err error) {
		defer func() {
			if err != nil {
				err = fmt.Errorf("client connect: %w", err)
			}
		}()

		remote := lo.Must(cmd.Flags().GetString("remote"))

		clientConn, err = grpc.Dial(
			remote,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.UseCompressor(gzip.Name)),
		)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}

		client = proto.NewAlfredClient(clientConn)
		return nil
	},

	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		if clientConn != nil {
			return clientConn.Close()
		}
		return nil
	},
}

func init() {
	alfredCmd.AddCommand(runCmd)
	alfredCmd.AddCommand(topCmd)
	alfredCmd.AddCommand(versionCmd)

	alfredCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	alfredCmd.PersistentFlags().String("remote", lo.Must(lo.Coalesce(os.Getenv("ALFRED_REMOTE"), "alfred.tipee.dev:25373")), "the server remote address")
}

func main() {
	if err := alfredCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, color.HiRedString(fmt.Sprint(err)))
		os.Exit(1)
	}
}
