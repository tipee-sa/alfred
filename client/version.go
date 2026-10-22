package main

import (
	"math"

	"github.com/gammadia/alfred/proto"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the version number of Alfred",
	Long: `Prints the client's version, then the server's. The client's line comes first,
before connecting, so it shows even when the server cannot be reached.`,

	// Print the client's version before connecting, so that it shows even when
	// the server cannot be reached.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cmd.Printf("alfred version %s (%s)\n", version, commit[:int(math.Min(float64(len(commit)), 7))])
		return alfredCmd.PersistentPreRunE(cmd, args)
	},

	RunE: func(cmd *cobra.Command, args []string) error {
		if response, err := client.Ping(cmd.Context(), &proto.PingRequest{}); err != nil {
			return err
		} else {
			cmd.Printf("server version %s (%s)\n", response.Version, response.Commit[:int(math.Min(float64(len(response.Commit)), 7))])
			return nil
		}
	},
}
