package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"github.com/gammadia/alfred/server/log"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMain(m *testing.M) {
	// Initialize the logger so server code doesn't panic on log calls
	log.Base = slog.New(slog.NewTextHandler(io.Discard, nil))
	// Use reflection-free init: set the unexported logger via the public Init path
	// is not possible without viper, so we call the log functions' underlying logger directly.
	// Instead, just call Init after setting viper defaults.
	os.Setenv("ALFRED_LOG_LEVEL", "ERROR")
	os.Setenv("ALFRED_LOG_FORMAT", "text")
	_ = log.Init()
	os.Exit(m.Run())
}

// --- lastNLines tests ---

func TestLastNLines_BasicTail(t *testing.T) {
	data := []byte("line1\nline2\nline3\nline4\nline5\n")
	result := string(lastNLines(data, 3))
	assert.Equal(t, "line3\nline4\nline5\n", result)
}

func TestLastNLines_FewerLinesThanRequested(t *testing.T) {
	data := []byte("line1\nline2\n")
	result := string(lastNLines(data, 10))
	assert.Equal(t, "line1\nline2\n", result)
}

func TestLastNLines_ExactLineCount(t *testing.T) {
	data := []byte("line1\nline2\nline3\n")
	result := string(lastNLines(data, 3))
	assert.Equal(t, "line1\nline2\nline3\n", result)
}

func TestLastNLines_SingleLine(t *testing.T) {
	data := []byte("only line\n")
	result := string(lastNLines(data, 1))
	assert.Equal(t, "only line\n", result)
}

func TestLastNLines_NoTrailingNewline(t *testing.T) {
	data := []byte("line1\nline2\nline3")
	result := string(lastNLines(data, 2))
	assert.Equal(t, "line2\nline3", result)
}

func TestLastNLines_EmptyData(t *testing.T) {
	result := lastNLines([]byte{}, 5)
	assert.Empty(t, result)
}

func TestLastNLines_NilData(t *testing.T) {
	result := lastNLines(nil, 5)
	assert.Nil(t, result)
}

func TestLastNLines_ZeroLines(t *testing.T) {
	data := []byte("line1\nline2\n")
	result := string(lastNLines(data, 0))
	assert.Equal(t, "line1\nline2\n", result, "n=0 returns all data")
}

func TestLastNLines_NegativeLines(t *testing.T) {
	data := []byte("line1\nline2\n")
	result := string(lastNLines(data, -1))
	assert.Equal(t, "line1\nline2\n", result, "n<0 returns all data")
}

func TestLastNLines_WithTailHeaders(t *testing.T) {
	// Simulates tail -v output with file headers
	data := []byte("==> step-1.log <==\nline1\nline2\n\n==> step-2.log <==\nlineA\nlineB\n")
	result := string(lastNLines(data, 3))
	assert.Equal(t, "==> step-2.log <==\nlineA\nlineB\n", result)
}

func TestLastNLines_OnlyNewlines(t *testing.T) {
	data := []byte("\n\n\n\n\n")
	result := string(lastNLines(data, 2))
	assert.Equal(t, "\n\n", result)
}

func TestLastNLines_OneLineNoNewline(t *testing.T) {
	data := []byte("hello")
	result := string(lastNLines(data, 1))
	assert.Equal(t, "hello", result)
}

func TestLastNLines_OneLineRequestMoreThanAvailable(t *testing.T) {
	data := []byte("hello\n")
	result := string(lastNLines(data, 100))
	assert.Equal(t, "hello\n", result)
}

// --- findNewContent tests ---

// --- streamReader tests ---

// mockLogStream implements proto.Alfred_StreamTaskLogsServer for testing.
type mockLogStream struct {
	chunks []*proto.StreamTaskLogsChunk
	ctx    context.Context
}

func (m *mockLogStream) Send(chunk *proto.StreamTaskLogsChunk) error {
	// Copied like gRPC serializes it: streamReader reuses its buffer.
	m.chunks = append(m.chunks, &proto.StreamTaskLogsChunk{
		Data:   bytes.Clone(chunk.Data),
		Length: chunk.Length,
	})
	return nil
}
func (m *mockLogStream) Context() context.Context     { return m.ctx }
func (m *mockLogStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockLogStream) SendHeader(metadata.MD) error { return nil }
func (m *mockLogStream) SetTrailer(metadata.MD)       {}
func (m *mockLogStream) SendMsg(interface{}) error    { return nil }
func (m *mockLogStream) RecvMsg(interface{}) error    { return nil }

func TestStreamReader_SendsAllContent(t *testing.T) {
	content := "line1\nline2\nline3\n"
	reader := io.NopCloser(strings.NewReader(content))

	mock := &mockLogStream{ctx: context.Background()}
	err := streamReader(mock, reader, "test-job", "test-task")

	require.NoError(t, err)
	assert.NotEmpty(t, mock.chunks)

	var received bytes.Buffer
	for _, chunk := range mock.chunks {
		received.Write(chunk.Data)
		assert.Equal(t, uint32(len(chunk.Data)), chunk.Length)
	}
	assert.Equal(t, content, received.String())
}

func TestStreamReader_EmptyReader(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(""))

	mock := &mockLogStream{ctx: context.Background()}
	err := streamReader(mock, reader, "test-job", "test-task")

	require.NoError(t, err)
	assert.Empty(t, mock.chunks)
}

func TestStreamReader_LargeContent_MultipleChunks(t *testing.T) {
	// Create content larger than the 32KB chunk buffer
	content := strings.Repeat("x", 100*1024) // 100KB
	reader := io.NopCloser(strings.NewReader(content))

	mock := &mockLogStream{ctx: context.Background()}
	err := streamReader(mock, reader, "test-job", "test-task")

	require.NoError(t, err)
	assert.Greater(t, len(mock.chunks), 1, "large content should be sent in multiple chunks")

	var received bytes.Buffer
	for _, chunk := range mock.chunks {
		received.Write(chunk.Data)
	}
	assert.Equal(t, content, received.String())
}

func TestSendLogData_SplitsWholeLogsUnderMaxPacketSize(t *testing.T) {
	// Follow mode's output goes through sendLogData: one message for whole
	// logs (`logs -f -n all`) would exceed what the client accepts.
	data := bytes.Repeat([]byte("0123456789abcde\n"), (config.MaxPacketSize+1024*1024)/16)

	mock := &mockLogStream{ctx: context.Background()}
	require.NoError(t, sendLogData(mock, data))

	var received bytes.Buffer
	for _, chunk := range mock.chunks {
		require.LessOrEqual(t, len(chunk.Data), logChunkSize)
		received.Write(chunk.Data)
	}
	assert.Greater(t, len(mock.chunks), 1)
	assert.True(t, bytes.Equal(data, received.Bytes()), "every byte is sent, in order")
}

func TestSendLogData_Empty(t *testing.T) {
	mock := &mockLogStream{ctx: context.Background()}
	require.NoError(t, sendLogData(mock, nil))
	assert.Empty(t, mock.chunks)
}

// --- extractLogsFromArtifact tests ---

// createTestArtifact builds a tar.zst archive containing the given files.
// Files is a map of archive-path -> content.
func createTestArtifact(t *testing.T, files map[string]string) string {
	t.Helper()

	f, err := os.CreateTemp("", "test-artifact-*.tar.zst")
	require.NoError(t, err)
	t.Cleanup(func() { os.Remove(f.Name()) })

	zw, err := zstd.NewWriter(f)
	require.NoError(t, err)

	tw := tar.NewWriter(zw)
	for name, content := range files {
		err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(content)),
			Mode:     0644,
			Typeflag: tar.TypeReg,
		})
		require.NoError(t, err)
		_, err = tw.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())

	return f.Name()
}

func TestExtractLogsFromArtifact_SingleLogFile(t *testing.T) {
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/task-step-0.log": "line1\nline2\nline3\n",
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, 100)
	require.NoError(t, err)
	defer r.Close()

	content, _ := io.ReadAll(r)
	assert.Equal(t, "==> task-step-0.log <==\nline1\nline2\nline3\n", string(content))
}

func TestExtractLogsFromArtifact_MultipleLogFiles(t *testing.T) {
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/task-step-0.log": "step0-line1\nstep0-line2\n",
		"job-task/output/task-step-1.log": "step1-line1\nstep1-line2\n",
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, 100)
	require.NoError(t, err)
	defer r.Close()

	content, _ := io.ReadAll(r)
	expected := "==> task-step-0.log <==\nstep0-line1\nstep0-line2\n\n==> task-step-1.log <==\nstep1-line1\nstep1-line2\n"
	assert.Equal(t, expected, string(content))
}

func TestExtractLogsFromArtifact_TailsLines(t *testing.T) {
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/task-step-0.log": "line1\nline2\nline3\nline4\nline5\n",
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, 2)
	require.NoError(t, err)
	defer r.Close()

	content, _ := io.ReadAll(r)
	assert.Equal(t, "==> task-step-0.log <==\nline4\nline5\n", string(content))
}

func TestExtractLogsFromArtifact_SkipsNonLogFiles(t *testing.T) {
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/task-step-0.log": "log content\n",
		"job-task/output/data.txt":        "not a log\n",
		"job-task/output/result.json":     `{"ok":true}`,
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, 100)
	require.NoError(t, err)
	defer r.Close()

	content, _ := io.ReadAll(r)
	assert.Equal(t, "==> task-step-0.log <==\nlog content\n", string(content))
}

func TestExtractLogsFromArtifact_NoLogFiles(t *testing.T) {
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/data.txt": "no logs here\n",
	})

	_, err := extractLogsFromArtifact(context.Background(), artifact, 100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no .log files found")
}

func TestExtractLogsFromArtifact_NonexistentFile(t *testing.T) {
	_, err := extractLogsFromArtifact(context.Background(), "/nonexistent/path.tar.zst", 100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open artifact")
}

func TestExtractLogsFromArtifact_SortsFilesByName(t *testing.T) {
	// Deliberately insert in reverse order to verify sorting
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/task-step-2.log": "step2\n",
		"job-task/output/task-step-0.log": "step0\n",
		"job-task/output/task-step-1.log": "step1\n",
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, 100)
	require.NoError(t, err)
	defer r.Close()

	content, _ := io.ReadAll(r)
	expected := "==> task-step-0.log <==\nstep0\n\n==> task-step-1.log <==\nstep1\n\n==> task-step-2.log <==\nstep2\n"
	assert.Equal(t, expected, string(content))
}

// --- whole logs (TailLines < 0, `logs -n all`) ---

func TestRequestedTailLines(t *testing.T) {
	for tailLines, want := range map[int32]int{
		0:    defaultTailLines, // proto3 default: the field is not set
		1:    1,
		250:  250,
		-1:   allLines,
		-100: allLines,
	} {
		assert.Equal(t, want, requestedTailLines(&proto.StreamTaskLogsRequest{TailLines: tailLines}), "TailLines %d", tailLines)
	}
}

// numberedLines returns the lines "<prefix>-1\n" to "<prefix>-<n>\n".
func numberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s-%d\n", prefix, i)
	}
	return b.String()
}

// createOrderedTestArtifact is createTestArtifact with the archive order given,
// written to path.
func createOrderedTestArtifact(t *testing.T, artifactPath string, files [][2]string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(path.Dir(artifactPath), 0755))
	f, err := os.Create(artifactPath)
	require.NoError(t, err)
	defer f.Close()

	zw, err := zstd.NewWriter(f)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)
	for _, file := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: file[0], Size: int64(len(file[1])), Mode: 0644, Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(file[1]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())

	return artifactPath
}

func readAllAndClose(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer r.Close()
	content, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(content)
}

// Follow mode compares windows it reads whole, so whole logs are refused there
// rather than read into memory, even for a finished task.
func TestStreamTaskLogs_FollowRefusesWholeLogs(t *testing.T) {
	oldDataRoot := dataRoot
	dataRoot = t.TempDir()
	t.Cleanup(func() { dataRoot = oldDataRoot })
	createOrderedTestArtifact(t, path.Join(dataRoot, "artifacts", "job-abc", "task.tar.zst"), [][2]string{
		{"job-abc-task/output/task-step-0.log", "line\n"},
	})

	mock := &mockLogStream{ctx: context.Background()}
	err := (&server{}).StreamTaskLogs(&proto.StreamTaskLogsRequest{Job: "job-abc", Task: "task", TailLines: -1, Follow: true}, mock)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Empty(t, mock.chunks)
}

// A running task's logs are read whole before anything is sent. If they
// vanish meanwhile (the task ended, its workspace deleted), nothing partial is
// sent: the artifact, archived before the workspace went, is sent instead.
func TestStreamTaskLogs_LiveLogsReadWholeFirst(t *testing.T) {
	oldDataRoot, oldRead := dataRoot, readLiveTaskLogs
	t.Cleanup(func() { dataRoot, readLiveTaskLogs = oldDataRoot, oldRead })
	dataRoot = t.TempDir()
	artifact := path.Join(dataRoot, "artifacts", "job-abc", "task.tar.zst")
	live := "==> task-step-0.log <==\n" + numberedLines("live", 50)

	stream := func() (string, error) {
		mock := &mockLogStream{ctx: context.Background()}
		err := (&server{}).StreamTaskLogs(&proto.StreamTaskLogsRequest{Job: "job-abc", Task: "task", TailLines: -1}, mock)
		var received bytes.Buffer
		for _, chunk := range mock.chunks {
			received.Write(chunk.Data)
		}
		return received.String(), err
	}

	// The task still running: its live logs.
	readLiveTaskLogs = func(job, task string, lines int) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(live)), nil
	}
	got, err := stream()
	require.NoError(t, err)
	assert.Equal(t, live, got)

	// The logs vanish halfway, with no artifact: an error, nothing partial.
	vanishing := func(onVanish func()) func(job, task string, lines int) (io.ReadCloser, error) {
		return func(job, task string, lines int) (io.ReadCloser, error) {
			return io.NopCloser(&vanishingReader{data: strings.NewReader(live[:100]), onVanish: onVanish}), nil
		}
	}
	readLiveTaskLogs = vanishing(func() {})
	got, err = stream()
	assert.Error(t, err)
	assert.Empty(t, got)

	// The logs vanish halfway as the task ends and its artifact is saved: the
	// artifact's logs, and none of the live read.
	readLiveTaskLogs = vanishing(func() {
		createOrderedTestArtifact(t, artifact, [][2]string{{"job-abc-task/output/task-step-0.log", numberedLines("final", 80)}})
	})
	got, err = stream()
	require.NoError(t, err)
	assert.Equal(t, "==> task-step-0.log <==\n"+numberedLines("final", 80), got)
}

// vanishingReader reads data, then fails like tail on a log deleted with the
// workspace, after running onVanish (which may save the artifact).
type vanishingReader struct {
	data     *strings.Reader
	onVanish func()
}

func (v *vanishingReader) Read(p []byte) (int, error) {
	if v.data.Len() > 0 {
		return v.data.Read(p)
	}
	v.onVanish()
	return 0, errors.New("tail: cannot open 'task-step-1.log' for reading: No such file or directory")
}

func TestStreamTaskLogs_NegativeTailLinesReturnsWholeLog(t *testing.T) {
	oldDataRoot := dataRoot
	dataRoot = t.TempDir()
	t.Cleanup(func() { dataRoot = oldDataRoot })

	stepLog := numberedLines("line", 250)
	createOrderedTestArtifact(t, path.Join(dataRoot, "artifacts", "job-abc", "task.tar.zst"), [][2]string{
		{"job-abc-task/output/task-step-0.log", stepLog},
	})

	stream := func(tailLines int32) string {
		mock := &mockLogStream{ctx: context.Background()}
		require.NoError(t, (&server{}).StreamTaskLogs(&proto.StreamTaskLogsRequest{Job: "job-abc", Task: "task", TailLines: tailLines}, mock))
		var received bytes.Buffer
		for _, chunk := range mock.chunks {
			received.Write(chunk.Data)
		}
		return received.String()
	}

	header := "==> task-step-0.log <==\n"
	assert.Equal(t, header+stepLog, stream(-1), "a negative TailLines asks for the whole log")
	assert.Equal(t, header+string(lastNLines([]byte(stepLog), 100)), stream(0), "an unset TailLines means 100 lines")
	assert.Equal(t, header+"line-249\nline-250\n", stream(2))
}

// Logs archived in reverse step order are output in step order with two passes
// over the archive (list, then output), not one pass per log.
func TestExtractLogsFromArtifact_ReverseOrderTakesTwoPasses(t *testing.T) {
	var files [][2]string
	expectedAll, expectedTail := "", ""
	for i := 9; i >= 0; i-- {
		files = append(files, [2]string{fmt.Sprintf("job-task/output/task-step-%d.log", i), numberedLines(fmt.Sprintf("step%d", i), 5)})
	}
	for i := 0; i < 10; i++ {
		sep := ""
		if i > 0 {
			sep = "\n"
		}
		header := fmt.Sprintf("%s==> task-step-%d.log <==\n", sep, i)
		expectedAll += header + numberedLines(fmt.Sprintf("step%d", i), 5)
		expectedTail += header + string(lastNLines([]byte(numberedLines(fmt.Sprintf("step%d", i), 5)), 2))
	}
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), files)

	oldOpen := openArtifact
	t.Cleanup(func() { openArtifact = oldOpen })
	opens := 0
	openArtifact = func(name string) (io.ReadCloser, error) {
		opens++
		return oldOpen(name)
	}
	oldCreateSpool := createSpool
	t.Cleanup(func() { createSpool = oldCreateSpool })
	spools := 0
	createSpool = func() (*os.File, error) {
		spools++
		return oldCreateSpool()
	}

	for _, c := range []struct {
		tailLines int
		expected  string
		spools    int // one file for every whole log set aside, none for tails
	}{{allLines, expectedAll, 1}, {2, expectedTail, 0}} {
		opens, spools = 0, 0
		r, err := extractLogsFromArtifact(context.Background(), artifact, c.tailLines)
		require.NoError(t, err)
		assert.Equal(t, c.expected, readAllAndClose(t, r), "tailLines=%d", c.tailLines)
		assert.Equal(t, 2, opens, "tailLines=%d", c.tailLines)
		assert.Equal(t, c.spools, spools, "tailLines=%d", c.tailLines)
	}
}

func TestReadLastBytes(t *testing.T) {
	data := numberedLines("line", 100_000) // ~1.2 MB
	for _, limit := range []int{1, 1000, 64 * 1024, len(data), 2 * len(data)} {
		got, truncated, err := readLastBytes(iotest.HalfReader(strings.NewReader(data)), limit)
		require.NoError(t, err)
		assert.Equal(t, data[max(0, len(data)-limit):], string(got), "limit=%d", limit)
		assert.Equal(t, limit < len(data), truncated, "limit=%d", limit)
	}
}

func TestFinalFollowDelta(t *testing.T) {
	header := "==> task-step-0.log <==\n"
	seen := header + numberedLines("seen", 1000)

	// What was written after the last poll fits: exactly that, no notice.
	delta, err := finalFollowDelta([]byte(seen), strings.NewReader(seen+numberedLines("new", 1000)))
	require.NoError(t, err)
	assert.Equal(t, numberedLines("new", 1000), string(delta))

	// It doesn't fit: the end of it, after a notice saying the start is cut.
	unseen := numberedLines("new", followFinalMaxBytes/8) // ~ 2x the limit
	delta, err = finalFollowDelta([]byte(seen), strings.NewReader(seen+unseen))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(delta), followTruncatedNotice))
	kept := string(delta[len(followTruncatedNotice):])
	assert.Len(t, kept, followFinalMaxBytes)
	assert.True(t, strings.HasSuffix(unseen, kept))

	// Cut, even if what was cut may all have been seen: the notice still,
	// as matching lines cannot prove it. Here repeated lines make the last
	// poll's last lines match inside new output, after unseen output was cut.
	working := strings.Repeat("working\n", 1000)
	unseenRepeated := strings.Repeat("working\n", followFinalMaxBytes/8+1000) // ~ the limit and more
	delta, err = finalFollowDelta([]byte(header+working), strings.NewReader(header+working+unseenRepeated+"done\n"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(delta), followTruncatedNotice), "a cut always says so")
	assert.True(t, strings.HasSuffix(string(delta), "done\n"))
}

// A tail kept a while (set aside) must not hold on to the large buffer it was
// cut from, here grown by a long first line.
func TestReadTail_ReturnsItsOwnCopy(t *testing.T) {
	data := strings.Repeat("x", 4<<20) + "\nshort-1\nshort-2\n"
	tail, err := readTail(strings.NewReader(data), 2)
	require.NoError(t, err)
	assert.Equal(t, "short-1\nshort-2\n", string(tail))
	assert.Less(t, cap(tail), 1024)
}

// Closing the reader, or cancelling the RPC's context while a read waits on the
// pipe, stops the walk: out-of-order logs are not decompressed and set aside for
// nobody, and the waiting read gets an error rather than hang.
func TestExtractLogsFromArtifact_StopsTheWalk(t *testing.T) {
	// Incompressible logs in reverse order: all but the last archived are set
	// aside before anything is written to the pipe.
	rng := rand.New(rand.NewSource(1))
	var files [][2]string
	for i := 3; i >= 0; i-- {
		raw := make([]byte, 3<<20)
		rng.Read(raw)
		files = append(files, [2]string{fmt.Sprintf("job-task/output/task-step-%d.log", i), base64.StdEncoding.EncodeToString(raw)})
	}
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), files)
	info, err := os.Stat(artifact)
	require.NoError(t, err)

	for _, c := range []struct {
		name string
		stop func(r io.ReadCloser, cancel context.CancelFunc)
	}{
		{"reader closed", func(r io.ReadCloser, _ context.CancelFunc) { require.NoError(t, r.Close()) }},
		{"RPC cancelled", func(_ io.ReadCloser, cancel context.CancelFunc) { cancel() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			oldOpen := openArtifact
			t.Cleanup(func() { openArtifact = oldOpen })
			gate := make(chan struct{})   // holds the output pass until stopped
			walked := make(chan int64, 1) // bytes the output pass read from the archive
			opens := 0
			openArtifact = func(name string) (io.ReadCloser, error) {
				f, err := oldOpen(name)
				if err != nil {
					return nil, err
				}
				if opens++; opens == 1 {
					return f, nil // listing the logs
				}
				return &gatedReader{r: f, gate: gate, done: walked}, nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, err := extractLogsFromArtifact(ctx, artifact, allLines)
			require.NoError(t, err)
			readErr := make(chan error, 1)
			go func() { // a read waiting on the pipe, as streamReader's
				_, err := io.ReadAll(r)
				readErr <- err
			}()

			c.stop(r, cancel)
			close(gate)

			select {
			case read := <-walked:
				assert.Less(t, read, info.Size()/4, "the archive is %d bytes", info.Size())
			case <-time.After(10 * time.Second):
				t.Fatal("the walk did not end")
			}
			select {
			case err := <-readErr:
				assert.Error(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the waiting read did not end")
			}
		})
	}
}

// gatedReader holds its reads until gate is closed, then counts them, and
// reports the count on done when closed.
type gatedReader struct {
	r    io.ReadCloser
	gate chan struct{}
	done chan int64
	read int64
}

func (g *gatedReader) Read(p []byte) (int, error) {
	<-g.gate
	n, err := g.r.Read(p)
	g.read += int64(n)
	return n, err
}

func (g *gatedReader) Close() error {
	g.done <- g.read
	return g.r.Close()
}

func TestExtractLogsFromArtifact_AllLinesInStepOrder(t *testing.T) {
	// Archived out of step order, so that it takes more than one pass.
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), [][2]string{
		{"job-task/output/task-step-2.log", numberedLines("step2", 3)},
		{"job-task/output/result.json", `{"ok":true}`},
		{"job-task/output/task-step-0.log", numberedLines("step0", 300)},
		{"job-task/output/task-step-1.log", "no final newline"},
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, allLines)
	require.NoError(t, err)

	expected := "==> task-step-0.log <==\n" + numberedLines("step0", 300) +
		"\n==> task-step-1.log <==\nno final newline\n" +
		"\n==> task-step-2.log <==\n" + numberedLines("step2", 3)
	assert.Equal(t, expected, readAllAndClose(t, r))
}

func TestExtractLogsFromArtifact_LogOver10MB(t *testing.T) {
	// Logs used to be read whole, up to 10MB: the tail of a longer one showed
	// lines from the middle, and nothing past 10MB could be read.
	stepLog := numberedLines("a fairly long line of test output, number", 300000)
	require.Greater(t, len(stepLog), 10*1024*1024)
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), [][2]string{
		{"job-task/output/task-step-0.log", stepLog},
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, 2)
	require.NoError(t, err)
	assert.Equal(t, "==> task-step-0.log <==\n"+
		"a fairly long line of test output, number-299999\n"+
		"a fairly long line of test output, number-300000\n", readAllAndClose(t, r))

	r, err = extractLogsFromArtifact(context.Background(), artifact, allLines)
	require.NoError(t, err)
	all := readAllAndClose(t, r)
	assert.Equal(t, len("==> task-step-0.log <==\n")+len(stepLog), len(all))
	assert.True(t, strings.HasSuffix(all, stepLog))
}

func TestExtractLogsFromArtifact_CloseBeforeEnd(t *testing.T) {
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), [][2]string{
		{"job-task/output/task-step-0.log", numberedLines("line", 100000)},
	})

	r, err := extractLogsFromArtifact(context.Background(), artifact, allLines)
	require.NoError(t, err)
	_, err = io.ReadFull(r, make([]byte, 10))
	require.NoError(t, err)
	require.NoError(t, r.Close())

	_, err = r.Read(make([]byte, 10))
	assert.ErrorIs(t, err, io.ErrClosedPipe)
}

func TestReadTail_MatchesLastNLines(t *testing.T) {
	// Several times tailTrimThreshold, read in uneven chunks.
	content := numberedLines("line", 400000)
	require.Greater(t, len(content), 3*tailTrimThreshold)

	for _, data := range []string{content, content + "partial last line"} {
		for _, n := range []int{1, 7, 1000, 150000, 1000000} {
			tail, err := readTail(iotest.HalfReader(strings.NewReader(data)), n)
			require.NoError(t, err)
			assert.Equal(t, string(lastNLines([]byte(data), n)), string(tail), "n=%d", n)
		}
	}
}

func TestReadTail_ReadError(t *testing.T) {
	_, err := readTail(iotest.ErrReader(errors.New("boom")), 10)
	assert.ErrorContains(t, err, "boom")
}

func TestFindNewContent_EmptyPrevious(t *testing.T) {
	assert.Equal(t, []byte("new"), findNewContent(nil, []byte("new")))
	assert.Equal(t, []byte("new"), findNewContent([]byte{}, []byte("new")))
}

func TestFindNewContent_Identical(t *testing.T) {
	data := []byte("line1\nline2\n")
	assert.Nil(t, findNewContent(data, data))
}

func TestFindNewContent_PrefixMatch(t *testing.T) {
	prev := []byte("line1\nline2\n")
	curr := []byte("line1\nline2\nline3\nline4\n")
	assert.Equal(t, []byte("line3\nline4\n"), findNewContent(prev, curr))
}

func TestFindNewContent_TailWindowShifted(t *testing.T) {
	// Simulates the case where the tail window moved: previous starts at line1,
	// but current starts at line2 (line1 fell off the tail window)
	prev := []byte("line1\nline2\nline3\n")
	curr := []byte("line2\nline3\nline4\nline5\n")
	assert.Equal(t, []byte("line4\nline5\n"), findNewContent(prev, curr))
}

func TestFindNewContent_NoOverlap(t *testing.T) {
	// Gap too large, no overlap — should return everything (safe fallback)
	prev := []byte("old content\n")
	curr := []byte("completely different content\n")
	assert.Equal(t, curr, findNewContent(prev, curr))
}

func TestFindNewContent_LiveToArtifactTransition(t *testing.T) {
	// With the TailLogs fix (basenames in headers), live logs and artifact
	// extraction now use the same header format. Verify findNewContent handles
	// the transition correctly: the live snapshot has 2 lines, the artifact
	// has all 3 lines, and we should get only the new line.
	prev := []byte("==> task-step-0.log <==\nline1\nline2\n")
	curr := []byte("==> task-step-0.log <==\nline1\nline2\nline3\n")
	delta := findNewContent(prev, curr)
	assert.Equal(t, []byte("line3\n"), delta, "should return only the new line appended after the live snapshot")
}

func TestFindNewContent_LiveToArtifactTransition_MultipleFiles(t *testing.T) {
	// Transition with multiple step files: live had partial output,
	// artifact has complete output including a new step file.
	prev := []byte("==> task-step-0.log <==\nstep0-line1\nstep0-line2\n")
	curr := []byte("==> task-step-0.log <==\nstep0-line1\nstep0-line2\n\n==> task-step-1.log <==\nstep1-line1\n")
	delta := findNewContent(prev, curr)
	assert.Equal(t, []byte("\n==> task-step-1.log <==\nstep1-line1\n"), delta,
		"should return the new step file content")
}
