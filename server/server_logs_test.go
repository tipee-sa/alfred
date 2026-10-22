package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"google.golang.org/grpc/metadata"
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

func TestFindNewContent_EmptyPrevious(t *testing.T) {
	assert.Equal(t, []byte("new"), findNewContent(nil, []byte("new")))
	assert.Equal(t, []byte("new"), findNewContent([]byte{}, []byte("new")))
}

// A previous snapshot holding no line, just "\n", yields no fingerprint: comparing it
// with content that doesn't start with it must not panic (it took the server down).
func TestNewContentOffset_NewlineOnlyPrevious(t *testing.T) {
	curr := "==> /alfred/output/task-step-1.log <==\nhello\n"
	offset, err := newContentOffset([]byte("\n"), strings.NewReader(curr))
	require.NoError(t, err)
	assert.Equal(t, int64(0), offset)
	assert.Equal(t, []byte(curr), findNewContent([]byte("\n"), []byte(curr)))
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

func TestFindNewContent_UnchangedPartialLastLine(t *testing.T) {
	// A trimmed initial snapshot (no header) ending with a line still being
	// written, then a poll of the same, unchanged output with its header.
	prev := []byte("line1\nline2\nline3\npart")
	curr := []byte("==> task-step-0.log <==\nline1\nline2\nline3\npart")
	assert.Empty(t, findNewContent(prev, curr), "nothing changed")

	curr = []byte("==> task-step-0.log <==\nline1\nline2\nline3\npartial line\nline5\n")
	assert.Equal(t, "ial line\nline5\n", string(findNewContent(prev, curr)), "only what is appended to the partial line")
}

func TestNewContentOffset_StreamedInSmallReads(t *testing.T) {
	// Fingerprints spanning reads: the same offsets byte by byte as in one read.
	prev := []byte("==> f.log <==\nl1\nl2\nl3\nl4\nl5\nl6\npa")
	cases := map[string]string{
		"prefix":         string(prev) + "rtial\nl8\n",
		"identical":      string(prev),
		"shorter":        string(prev[:20]),
		"window shifted": "==> f.log <==\nl3\nl4\nl5\nl6\npartial\nl8\n",
		"3 lines twice":  "xx\nl5\nl6\npa\nl8\nl5\nl6\npa\nl9\n",
		"2 lines only":   "l6\npa\nl8\n",
		"last line only": "xx\npa\nl8\n",
		"no overlap":     "something else\n",
	}
	for name, curr := range cases {
		t.Run(name, func(t *testing.T) {
			whole, err := newContentOffset(prev, strings.NewReader(curr))
			require.NoError(t, err)
			oneByte, err := newContentOffset(prev, iotest.OneByteReader(strings.NewReader(curr)))
			require.NoError(t, err)
			assert.Equal(t, whole, oneByte)
		})
	}

	offset, err := newContentOffset(prev, iotest.OneByteReader(strings.NewReader(cases["3 lines twice"])))
	require.NoError(t, err)
	assert.Equal(t, "\nl8\nl5\nl6\npa\nl9\n", cases["3 lines twice"][offset:], "after the first occurrence of the last 3 lines")
	offset, err = newContentOffset(prev, iotest.OneByteReader(strings.NewReader(cases["prefix"])))
	require.NoError(t, err)
	assert.Equal(t, "rtial\nl8\n", cases["prefix"][offset:])
}

func TestNewContentOffset_ReadError(t *testing.T) {
	_, err := newContentOffset([]byte("l1\n"), iotest.ErrReader(errors.New("boom")))
	assert.ErrorContains(t, err, "boom")
}

func TestFindNewContent_NoOverlap(t *testing.T) {
	// Gap too large, no overlap — should return everything (safe fallback)
	prev := []byte("old content\n")
	curr := []byte("completely different content\n")
	assert.Equal(t, curr, findNewContent(prev, curr))
}

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

func TestReadLastBytes(t *testing.T) {
	content := numberedLines("line", 1000)

	got, err := readLastBytes(strings.NewReader(content), len(content))
	require.NoError(t, err)
	assert.Equal(t, content, string(got), "all of it when it fits")

	got, err = readLastBytes(iotest.HalfReader(strings.NewReader(content)), 100)
	require.NoError(t, err)
	want := content[len(content)-100:]
	want = want[strings.IndexByte(want, '\n')+1:]
	assert.Equal(t, want, string(got), "the last whole lines that fit")
}

// setUpFollow makes followLiveLogs testable without a scheduler: live reads
// return what live returns, and polls come at once.
func setUpFollow(t *testing.T, live func(call, lines int) (io.ReadCloser, error)) *[]int {
	t.Helper()
	oldDataRoot, oldRead, oldInterval := dataRoot, readLiveTaskLogs, followPollInterval
	t.Cleanup(func() { dataRoot, readLiveTaskLogs, followPollInterval = oldDataRoot, oldRead, oldInterval })
	dataRoot = t.TempDir()
	followPollInterval = time.Millisecond

	var requested []int
	readLiveTaskLogs = func(job, task string, lines int) (io.ReadCloser, error) {
		requested = append(requested, lines)
		return live(len(requested), lines)
	}
	return &requested
}

func receivedChunks(t *testing.T, mock *mockLogStream) string {
	t.Helper()
	var received bytes.Buffer
	for _, chunk := range mock.chunks {
		require.LessOrEqual(t, len(chunk.Data), logChunkSize)
		received.Write(chunk.Data)
	}
	return received.String()
}

var errTornDown = errors.New("workspace torn down")

func TestFollowLiveLogs_AllStartsWithWindowThenWholeFinalDelta(t *testing.T) {
	// `-f -n all` starts with the last followWindowLines lines; then, before
	// the task completes, more new lines than a poll reads, in that step log
	// and in a new one: none of them may be lost.
	header := "==> task-step-0.log <==\n"
	live := numberedLines("line", 2*followWindowLines)
	requested := setUpFollow(t, func(call, lines int) (io.ReadCloser, error) {
		if call == 1 {
			return io.NopCloser(strings.NewReader(header + string(lastNLines([]byte(live), lines)))), nil
		}
		return nil, errTornDown
	})
	final := numberedLines("line", 3*followWindowLines+5)
	step1 := numberedLines("step1", followWindowLines+5)
	createOrderedTestArtifact(t, path.Join(dataRoot, "artifacts", "job-abc", "task.tar.zst"), [][2]string{
		{"job-abc-task/output/task-step-0.log", final},
		{"job-abc-task/output/task-step-1.log", step1},
	})

	mock := &mockLogStream{ctx: context.Background()}
	require.NoError(t, (&server{}).followLiveLogs(mock, "job-abc", "task", allLines))

	assert.Equal(t,
		header+string(lastNLines([]byte(final), 2*followWindowLines+5))+"\n==> task-step-1.log <==\n"+step1,
		receivedChunks(t, mock),
		"the last lines a poll reads, then all that the artifact adds")
	assert.Equal(t, []int{followWindowLines, followWindowLines}, *requested)
}

func TestFollowLiveLogs_ReadFailingMidwayFallsBackToArtifact(t *testing.T) {
	// The workspace torn down while the first read drains: tail has output a
	// first step log, then fails on the next one.
	requested := setUpFollow(t, func(call, lines int) (io.ReadCloser, error) {
		if call == 1 {
			return io.NopCloser(io.MultiReader(
				strings.NewReader("==> task-step-0.log <==\nline-1\n"),
				iotest.ErrReader(errTornDown),
			)), nil
		}
		return nil, errTornDown
	})
	createOrderedTestArtifact(t, path.Join(dataRoot, "artifacts", "job-abc", "task.tar.zst"), [][2]string{
		{"job-abc-task/output/task-step-0.log", "line-1\nline-2\n"},
		{"job-abc-task/output/task-step-1.log", "x\n"},
	})

	mock := &mockLogStream{ctx: context.Background()}
	require.NoError(t, (&server{}).followLiveLogs(mock, "job-abc", "task", 100))

	assert.Equal(t, "==> task-step-0.log <==\nline-1\nline-2\n\n==> task-step-1.log <==\nx\n", receivedChunks(t, mock),
		"the logs from the artifact, nothing sent twice")
	assert.Equal(t, []int{100}, *requested)
}

func TestFollowLiveLogs_UnchangedRepetitiveOutputSentOnce(t *testing.T) {
	// `-f -n 10000` on 10,000 identical lines: the polls read the same output,
	// so there is nothing new to send, nor at the end from the artifact.
	header := "==> task-step-0.log <==\n"
	waiting := strings.Repeat("waiting\n", followWindowLines)
	setUpFollow(t, func(call, lines int) (io.ReadCloser, error) {
		if call <= 3 {
			return io.NopCloser(strings.NewReader(header + waiting)), nil
		}
		return nil, errTornDown
	})
	createOrderedTestArtifact(t, path.Join(dataRoot, "artifacts", "job-abc", "task.tar.zst"), [][2]string{
		{"job-abc-task/output/task-step-0.log", waiting},
	})

	mock := &mockLogStream{ctx: context.Background()}
	require.NoError(t, (&server{}).followLiveLogs(mock, "job-abc", "task", followWindowLines))

	assert.Equal(t, header+waiting, receivedChunks(t, mock))
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

	r, err := extractLogsFromArtifact(artifact, 100)
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

	r, err := extractLogsFromArtifact(artifact, 100)
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

	r, err := extractLogsFromArtifact(artifact, 2)
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

	r, err := extractLogsFromArtifact(artifact, 100)
	require.NoError(t, err)
	defer r.Close()

	content, _ := io.ReadAll(r)
	assert.Equal(t, "==> task-step-0.log <==\nlog content\n", string(content))
}

func TestExtractLogsFromArtifact_NoLogFiles(t *testing.T) {
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/data.txt": "no logs here\n",
	})

	_, err := extractLogsFromArtifact(artifact, 100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no .log files found")
}

func TestExtractLogsFromArtifact_NonexistentFile(t *testing.T) {
	_, err := extractLogsFromArtifact("/nonexistent/path.tar.zst", 100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open artifact")
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

func TestExtractLogsFromArtifact_SortsFilesByName(t *testing.T) {
	// Deliberately insert in reverse order to verify sorting
	artifact := createTestArtifact(t, map[string]string{
		"job-task/output/task-step-2.log": "step2\n",
		"job-task/output/task-step-0.log": "step0\n",
		"job-task/output/task-step-1.log": "step1\n",
	})

	r, err := extractLogsFromArtifact(artifact, 100)
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

func TestExtractLogsFromArtifact_AllLinesInStepOrder(t *testing.T) {
	// Archived out of step order, so that it takes more than one pass.
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), [][2]string{
		{"job-task/output/task-step-2.log", numberedLines("step2", 3)},
		{"job-task/output/result.json", `{"ok":true}`},
		{"job-task/output/task-step-0.log", numberedLines("step0", 300)},
		{"job-task/output/task-step-1.log", "no final newline"},
	})

	r, err := extractLogsFromArtifact(artifact, allLines)
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

	r, err := extractLogsFromArtifact(artifact, 2)
	require.NoError(t, err)
	assert.Equal(t, "==> task-step-0.log <==\n"+
		"a fairly long line of test output, number-299999\n"+
		"a fairly long line of test output, number-300000\n", readAllAndClose(t, r))

	r, err = extractLogsFromArtifact(artifact, allLines)
	require.NoError(t, err)
	all := readAllAndClose(t, r)
	assert.Equal(t, len("==> task-step-0.log <==\n")+len(stepLog), len(all))
	assert.True(t, strings.HasSuffix(all, stepLog))
}

func TestExtractLogsFromArtifact_CloseBeforeEnd(t *testing.T) {
	artifact := createOrderedTestArtifact(t, path.Join(t.TempDir(), "task.tar.zst"), [][2]string{
		{"job-task/output/task-step-0.log", numberedLines("line", 100000)},
	})

	r, err := extractLogsFromArtifact(artifact, allLines)
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
