package local

import (
	"io"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTailLogs(t *testing.T) {
	root := t.TempDir()
	f := newFs(root)

	// Create a subdirectory with log files
	logDir := "job-abc/output"
	require.NoError(t, os.MkdirAll(path.Join(root, logDir), 0755))

	// Write some log files (step ordering matters)
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "task-step-1.log"), []byte("line1-a\nline2-a\nline3-a\n"), 0644))
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "task-step-2.log"), []byte("line1-b\nline2-b\nline3-b\n"), 0644))

	rc, err := f.TailLogs(logDir, 2)
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)

	output := string(data)
	// With multiple files, tail adds headers like "==> filename <=="
	assert.Contains(t, output, "task-step-1.log")
	assert.Contains(t, output, "task-step-2.log")
	assert.Contains(t, output, "line2-a")
	assert.Contains(t, output, "line3-a")
	assert.Contains(t, output, "line2-b")
	assert.Contains(t, output, "line3-b")
	// line1-a and line1-b should NOT appear with -n 2
	assert.NotContains(t, output, "line1-a")
	assert.NotContains(t, output, "line1-b")
}

func TestTailLogsHeadersUseBasenames(t *testing.T) {
	root := t.TempDir()
	f := newFs(root)

	logDir := "job-hdr/output"
	require.NoError(t, os.MkdirAll(path.Join(root, logDir), 0755))
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "task-step-0.log"), []byte("hello\n"), 0644))
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "task-step-1.log"), []byte("world\n"), 0644))

	rc, err := f.TailLogs(logDir, 10)
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)

	output := string(data)
	// Headers must use basenames, not full paths (matches extractLogsFromArtifact format)
	assert.Contains(t, output, "==> task-step-0.log <==")
	assert.Contains(t, output, "==> task-step-1.log <==")
	assert.NotContains(t, output, root, "headers should not contain the full filesystem path")
}

func TestTailLogsSingleFile(t *testing.T) {
	root := t.TempDir()
	f := newFs(root)

	logDir := "job-xyz/output"
	require.NoError(t, os.MkdirAll(path.Join(root, logDir), 0755))
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "task-step-1.log"), []byte("alpha\nbeta\ngamma\ndelta\n"), 0644))

	rc, err := f.TailLogs(logDir, 2)
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)

	output := string(data)
	assert.Contains(t, output, "gamma")
	assert.Contains(t, output, "delta")
	assert.NotContains(t, output, "alpha")
	assert.NotContains(t, output, "beta")
}

func TestTailLogsNoLogFiles(t *testing.T) {
	root := t.TempDir()
	f := newFs(root)

	logDir := "job-empty/output"
	require.NoError(t, os.MkdirAll(path.Join(root, logDir), 0755))
	// Create a non-log file
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "readme.txt"), []byte("not a log"), 0644))

	_, err := f.TailLogs(logDir, 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no log files found")
}

func TestTailLogsNonexistentDir(t *testing.T) {
	root := t.TempDir()
	f := newFs(root)

	_, err := f.TailLogs("nonexistent/dir", 10)
	assert.Error(t, err)
}

func TestTailLogsIgnoresNonLogFiles(t *testing.T) {
	root := t.TempDir()
	f := newFs(root)

	logDir := "job-mixed/output"
	require.NoError(t, os.MkdirAll(path.Join(root, logDir), 0755))

	require.NoError(t, os.WriteFile(path.Join(root, logDir, "task-step-1.log"), []byte("log line\n"), 0644))
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "hello.txt"), []byte("not a log\n"), 0644))
	require.NoError(t, os.WriteFile(path.Join(root, logDir, "data.json"), []byte("{}\n"), 0644))

	rc, err := f.TailLogs(logDir, 10)
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)

	output := string(data)
	assert.Contains(t, output, "log line")
	assert.NotContains(t, output, "not a log")
	assert.NotContains(t, output, "{}")
}
