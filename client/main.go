package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/client/sossh"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Versioning information set at build time
var version, commit, repository = "dev", "n/a", "tipee-sa/alfred"

var clientConn *grpc.ClientConn
var client proto.AlfredClient

var verbose bool

var alfredCmd = &cobra.Command{
	Use:   "alfred",
	Short: "Alfred is a distributed job scheduler.",

	SilenceUsage:  true,
	SilenceErrors: true,

	PersistentPreRunE: func(cmd *cobra.Command, args []string) (err error) {
		dependencies := []string{"bash", "docker", "ssh", "zstd"}
		command := exec.Command("which", dependencies...)
		if err := command.Run(); err != nil {
			return fmt.Errorf("missing mandatory dependencies (%s): %w", strings.Join(dependencies, ", "), err)
		}

		defer func() {
			if err != nil {
				err = fmt.Errorf("failed to connect to gRPC client: %w", err)
			}
		}()

		remote := lo.Must(cmd.Flags().GetString("remote"))

		host, port, _ := strings.Cut(remote, ":")
		if port == "" {
			port = "25373"
		}
		sshTunneling := lo.Must(cmd.Flags().GetBool("ssh-tunneling"))
		if (host == "127.0.0.1" || host == "localhost") && !cmd.Flags().Changed("ssh-tunneling") {
			sshTunneling = false
		}

		clientConn, err = grpc.Dial(
			fmt.Sprintf("%s:%s", host, port),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(config.MaxPacketSize)),
			grpc.WithContextDialer(func(ctx context.Context, remote string) (net.Conn, error) {
				if !sshTunneling {
					return net.Dial("tcp", remote)
				}

				sshPort := lo.Must(cmd.Flags().GetInt("ssh-port"))
				return sossh.DialContext(
					cmd.Context(),
					"tcp",
					fmt.Sprintf("%s:%d", host, sshPort),
					lo.Must(cmd.Flags().GetString("ssh-username")),
					fmt.Sprintf("127.0.0.1:%s", port),
				)
			}),
		)
		if err != nil {
			return fmt.Errorf("failed to dial gRPC: %w", err)
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
	alfredCmd.AddCommand(artifactCmd)
	alfredCmd.AddCommand(cancelCmd)
	alfredCmd.AddCommand(psCmd)
	alfredCmd.AddCommand(runCmd)
	alfredCmd.AddCommand(selfUpdateCmd)
	alfredCmd.AddCommand(showCmd)
	alfredCmd.AddCommand(topCmd)
	alfredCmd.AddCommand(versionCmd)
	alfredCmd.AddCommand(watchCmd)

	alfredCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	alfredCmd.PersistentFlags().String("remote", lo.Must(lo.Coalesce(os.Getenv("ALFRED_REMOTE"), "alfred.tipee.dev:25373")), "the server remote address")
	alfredCmd.PersistentFlags().Bool("ssh-tunneling", true, "use ssh tunneling to connect to the server")
	alfredCmd.PersistentFlags().String("ssh-username", "alfred-user", "username to use for ssh tunneling")
	alfredCmd.PersistentFlags().Int("ssh-port", 22, "port to use for ssh tunneling")
	alfredCmd.PersistentFlags().String("ssh-host-key", "", "host key to use for ssh tunneling verification")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	alfredCmd.SetOut(os.Stdout)
	if err := alfredCmd.ExecuteContext(ctx); err != nil {
		lo.Must(fmt.Fprintln(os.Stderr, color.HiRedString(fmt.Sprint(err))))
		os.Exit(1)
	}
}
