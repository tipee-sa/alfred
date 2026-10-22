package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runPreRun parses args as `alfred run` would, then runs the root's
// PersistentPreRunE, restoring the flags and the connection afterwards.
func runPreRun(t *testing.T, args ...string) error {
	// PersistentPreRunE checks these are installed; stub the ones a test host may lack.
	dir := t.TempDir()
	for _, name := range []string{"docker", "ssh", "zstd"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Cleanup(func() {
		for _, name := range []string{"dry-run", "remote", "connect-timeout"} {
			f := runCmd.Flags().Lookup(name)
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
		if clientConn != nil {
			clientConn.Close()
		}
		clientConn, client = nil, nil
	})

	require.NoError(t, runCmd.ParseFlags(args))
	runCmd.SetContext(context.Background())
	return alfredCmd.PersistentPreRunE(runCmd, nil)
}

func TestDryRunDoesNotConnect(t *testing.T) {
	addr := startSilentListener(t)

	start := time.Now()
	err := runPreRun(t, "--dry-run", "--remote", addr, "--connect-timeout", "30s")
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "a dry run must not wait for the server")
	assert.Nil(t, clientConn, "a dry run must not connect")
}

func TestRunConnectsWithoutDryRun(t *testing.T) {
	// The counterpart of TestDryRunDoesNotConnect: the same setup does connect.
	addr := startSilentListener(t)

	err := runPreRun(t, "--remote", addr, "--connect-timeout", "300ms")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no answer from "+addr)
}
