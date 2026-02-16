package main

import "github.com/spf13/cobra"

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh]",
	Short: "Generate shell completion scripts",

	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

var completionBashCmd = &cobra.Command{
	Use:   "bash",
	Short: "Generate bash completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		return alfredCmd.GenBashCompletionV2(cmd.OutOrStdout(), true)
	},
}

var completionZshCmd = &cobra.Command{
	Use:   "zsh",
	Short: "Generate zsh completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		return alfredCmd.GenZshCompletion(cmd.OutOrStdout())
	},
}

func init() {
	completionCmd.AddCommand(completionBashCmd, completionZshCmd)
}
