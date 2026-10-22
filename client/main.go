package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

// Versioning information set at build time
var version, commit, repository = "dev", "n/a", "tipee-sa/alfred"

var clientConn *grpc.ClientConn
var client proto.AlfredClient

var verbose bool

var connectTimeoutDefault, connectTimeoutEnvErr = connectTimeoutFlagDefault()

var alfredCmd = &cobra.Command{
	Use:   "alfred",
	Short: "Alfred is a distributed job scheduler.",
	Long: `Alfred runs CI jobs on a pool of nodes. A job is described by a jobfile
(YAML): 'alfred run' builds its step images locally with Docker, uploads them
to the server, and the server runs each of the job's tasks on its nodes.

Scripts, CI and AI agents: see 'alfred help scripting'.`,

	SilenceUsage:  true,
	SilenceErrors: true,

	PersistentPreRunE: func(cmd *cobra.Command, args []string) (err error) {
		startUpdateCheck(cmd.Context())

		dependencies := []string{"bash", "docker", "ssh", "zstd"}
		command := exec.Command("which", dependencies...)
		if err := command.Run(); err != nil {
			return fmt.Errorf("missing mandatory dependencies (%s): %w", strings.Join(dependencies, ", "), err)
		}

		// `run --dry-run` only builds and prints the job: it needs no server, so
		// it must keep working when the server cannot be reached.
		if dryRun, err := cmd.Flags().GetBool("dry-run"); err == nil && dryRun {
			return nil
		}

		if connectTimeoutEnvErr != nil && !cmd.Flags().Changed("connect-timeout") {
			return connectTimeoutEnvErr
		}
		timeout := lo.Must(cmd.Flags().GetDuration("connect-timeout"))
		if timeout <= 0 {
			return fmt.Errorf("--connect-timeout must be positive, got %s", timeout)
		}

		remote := lo.Must(cmd.Flags().GetString("remote"))

		host, port, _ := strings.Cut(remote, ":")
		if port == "" {
			port = "25373"
		}
		sshTunneling := lo.Must(cmd.Flags().GetBool("ssh-tunneling"))
		if (host == "127.0.0.1" || host == "localhost") && !cmd.Flags().Changed("ssh-tunneling") {
			sshTunneling = false
		}

		clientConn, err = connect(cmd.Context(), connectOptions{
			host:         host,
			port:         port,
			sshTunneling: sshTunneling,
			sshUsername:  lo.Must(cmd.Flags().GetString("ssh-username")),
			sshPort:      lo.Must(cmd.Flags().GetInt("ssh-port")),
			timeout:      timeout,
		})
		if err != nil {
			return fmt.Errorf("failed to connect to server: %w", err)
		}

		client = proto.NewAlfredClient(clientConn)
		return nil
	},

	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		printUpdateNotice()

		if clientConn != nil {
			return clientConn.Close()
		}
		return nil
	},
}

func init() {
	alfredCmd.AddCommand(artifactCmd)
	alfredCmd.AddCommand(cancelCmd)
	alfredCmd.AddCommand(completionCmd)
	alfredCmd.AddCommand(logsCmd)
	alfredCmd.AddCommand(psCmd)
	alfredCmd.AddCommand(runCmd)
	alfredCmd.AddCommand(selfUpdateCmd)
	alfredCmd.AddCommand(showCmd)
	alfredCmd.AddCommand(topCmd)
	alfredCmd.AddCommand(versionCmd)
	alfredCmd.AddCommand(watchCmd)
	alfredCmd.AddCommand(scriptingHelp)

	alfredCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	alfredCmd.PersistentFlags().String("remote", lo.Must(lo.Coalesce(os.Getenv("ALFRED_REMOTE"), "alfred.tipee.dev:25373")), "the server remote address")
	alfredCmd.PersistentFlags().Bool("ssh-tunneling", true, "use ssh tunneling to connect to the server")
	alfredCmd.PersistentFlags().String("ssh-username", "alfred-user", "username to use for ssh tunneling")
	alfredCmd.PersistentFlags().Int("ssh-port", 22, "port to use for ssh tunneling")
	alfredCmd.PersistentFlags().Duration("connect-timeout", connectTimeoutDefault, "time allowed to establish the connection to the server, ssh tunnel included; does not limit commands once connected (env "+connectTimeoutEnv+")")
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
