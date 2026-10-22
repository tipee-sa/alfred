package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseWatchFlags(t *testing.T, args ...string) (opts watchOptions, stdout, stderr string) {
	t.Helper()
	cmd := &cobra.Command{}
	addWatchFlags(cmd)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	require.NoError(t, cmd.ParseFlags(args))
	opts = watchOptionsFromFlags(cmd)
	return opts, out.String(), errOut.String()
}

func TestWatchFlags_AbortOnWarning(t *testing.T) {
	opts, _, stderr := parseWatchFlags(t, "--abort-on-warning")

	assert.True(t, opts.abortOnWarning)
	assert.False(t, opts.abortOnError)
	assert.Empty(t, stderr)
}

func TestWatchFlags_AbortOnFailureIsADeprecatedAlias(t *testing.T) {
	opts, stdout, stderr := parseWatchFlags(t, "--abort-on-failure", "--json")

	assert.True(t, opts.abortOnWarning, "the old name keeps working")
	assert.Empty(t, stdout, "nothing on stdout, which carries --json")
	assert.Contains(t, stderr, "--abort-on-failure is deprecated, use --abort-on-warning")
	assert.True(t, watchCmd.Flags().Lookup("abort-on-failure").Hidden)
	assert.True(t, runCmd.Flags().Lookup("abort-on-failure").Hidden)
}
