package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// Every command, and the root, says more than its one-line summary in --help.
func TestEveryCommandHasALongDescription(t *testing.T) {
	var check func(cmd *cobra.Command)
	check = func(cmd *cobra.Command) {
		if cmd.Hidden || cmd.Name() == "help" {
			return
		}
		// completion's subcommands (bash, zsh) are covered by its own text.
		if cmd.Parent() == nil || cmd.Parent() == alfredCmd {
			assert.NotEmpty(t, cmd.Long, "alfred %s has no Long description", cmd.Name())
		}
		for _, sub := range cmd.Commands() {
			check(sub)
		}
	}
	check(alfredCmd)
}
