package internal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/scheduler"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// --- Mock Docker Client ---

type mockDocker struct {
	mu sync.Mutex

	// Track calls for assertions
	networksCreated  []string
	networksRemoved  []string
	containersCreated []string
	containersStarted []string
	containersRemoved []string
	containersKilled  []string

	// Control behavior
	containerWaitCh    chan container.WaitResponse
	containerWaitErrCh chan error
	stepExitCode       int64

	// Override specific behaviors
	networkCreateErr    error
	containerCreateErr  error
	containerStartErr   error
	containerRemoveErr  error
}

func newMockDocker() *mockDocker {
	waitCh := make(chan container.WaitResponse, 1)
	waitErrCh := make(chan error, 1)
	return &mockDocker{
		containerWaitCh:    waitCh,
		containerWaitErrCh: waitErrCh,
	}
}

func (m *mockDocker) NetworkCreate(_ context.Context, name string, _ network.CreateOptions) (network.CreateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.networkCreateErr != nil {
		return network.CreateResponse{}, m.networkCreateErr
	}
	m.networksCreated = append(m.networksCreated, name)
	return network.CreateResponse{ID: "net-" + name}, nil
}

func (m *mockDocker) NetworkRemove(_ context.Context, networkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.networksRemoved = append(m.networksRemoved, networkID)
	return nil
}

func (m *mockDocker) ContainerCreate(_ context.Context, config *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containerCreateErr != nil {
		return container.CreateResponse{}, m.containerCreateErr
	}
	m.containersCreated = append(m.containersCreated, containerName)
	return container.CreateResponse{ID: "ctr-" + containerName}, nil
}

func (m *mockDocker) ContainerStart(_ context.Context, containerID string, _ container.StartOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containerStartErr != nil {
		return m.containerStartErr
	}
	m.containersStarted = append(m.containersStarted, containerID)
	return nil
}

func (m *mockDocker) ContainerWait(_ context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	return m.containerWaitCh, m.containerWaitErrCh
}

func (m *mockDocker) ContainerKill(_ context.Context, containerID string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containersKilled = append(m.containersKilled, containerID)
	return nil
}

func (m *mockDocker) ContainerRemove(_ context.Context, containerID string, _ container.RemoveOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containerRemoveErr != nil {
		return m.containerRemoveErr
	}
	m.containersRemoved = append(m.containersRemoved, containerID)
	return nil
}

func (m *mockDocker) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("mock logs")), nil
}

func (m *mockDocker) ContainerExecCreate(_ context.Context, _ string, _ container.ExecOptions) (container.ExecCreateResponse, error) {
	return container.ExecCreateResponse{ID: "exec-1"}, nil
}

// mockConn is a minimal net.Conn for HijackedResponse.Close().
type mockConn struct{ net.Conn }

func (mockConn) Close() error { return nil }

func (m *mockDocker) ContainerExecAttach(_ context.Context, _ string, _ container.ExecStartOptions) (types.HijackedResponse, error) {
	return types.HijackedResponse{
		Conn:   mockConn{},
		Reader: bufio.NewReader(strings.NewReader("")),
	}, nil
}

func (m *mockDocker) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	return container.ExecInspect{ExitCode: 0}, nil
}

func (m *mockDocker) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return []image.Summary{{}}, nil // image already present
}

func (m *mockDocker) ImagePull(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// --- Mock WorkspaceFS ---

type mockFS struct {
	mu           sync.Mutex
	dirsCreated  []string
	deletedPaths []string
	hostPath     string
}

func newMockFS() *mockFS {
	return &mockFS{hostPath: "/mock/workspace"}
}

func (f *mockFS) HostPath(_ string) string { return f.hostPath }

func (f *mockFS) MkDir(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirsCreated = append(f.dirsCreated, p)
	return nil
}

func (f *mockFS) SaveContainerLogs(_, _ string) error { return nil }
func (f *mockFS) StreamContainerLogs(_ context.Context, _, _ string) error { return nil }

func (f *mockFS) Archive(_ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("archive-data"))), nil
}

func (f *mockFS) TailLogs(_ string, _ int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("log-data")), nil
}

func (f *mockFS) Delete(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedPaths = append(f.deletedPaths, p)
	return nil
}

func (f *mockFS) Scope(_ string) WorkspaceFS { return f }

// --- Helpers ---

func testTask(steps []string) *scheduler.Task {
	return &scheduler.Task{
		Job: &scheduler.Job{
			Job: &proto.Job{
				Name:  "test-job",
				Steps: steps,
			},
		},
		Name:  "task-1",
		Slots: 1,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func noopRunConfig() scheduler.RunTaskConfig {
	return scheduler.RunTaskConfig{}
}

// --- Tests ---

func TestRunContainer_SingleStepSuccess(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	// Step exits successfully
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// Verify network was created and cleaned up
	if len(docker.networksCreated) != 1 {
		t.Fatalf("expected 1 network created, got %d", len(docker.networksCreated))
	}
	if len(docker.networksRemoved) != 1 {
		t.Fatalf("expected 1 network removed, got %d", len(docker.networksRemoved))
	}

	// Verify container was created, started, and removed
	if len(docker.containersCreated) != 1 {
		t.Fatalf("expected 1 container created, got %d", len(docker.containersCreated))
	}
	if len(docker.containersStarted) != 1 {
		t.Fatalf("expected 1 container started, got %d", len(docker.containersStarted))
	}
	if len(docker.containersRemoved) != 1 {
		t.Fatalf("expected 1 container removed, got %d", len(docker.containersRemoved))
	}

	// Verify workspace was created and cleaned up
	if len(fs.deletedPaths) != 1 {
		t.Fatalf("expected workspace cleanup, got %d deletes", len(fs.deletedPaths))
	}
}

func TestRunContainer_StepFailure(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step1:latest", "step2:latest"})

	// First step exits with error
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 1}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(err.Error(), "step 1 failed with status: 1") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second step should NOT have been created (only 1 container for the failed step)
	if len(docker.containersCreated) != 1 {
		t.Fatalf("expected 1 container created (second step skipped), got %d", len(docker.containersCreated))
	}

	// Cleanup should still happen
	if len(docker.networksRemoved) != 1 {
		t.Fatalf("expected network cleanup, got %d removes", len(docker.networksRemoved))
	}
	if len(fs.deletedPaths) != 1 {
		t.Fatalf("expected workspace cleanup, got %d deletes", len(fs.deletedPaths))
	}
}

func TestRunContainer_MultipleSteps(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step1:latest", "step2:latest"})

	// Both steps succeed — queue two responses
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}
	// The second step needs its own wait channel since the mock only has one.
	// We need to send a second response after the first step completes.
	// Since ContainerWait returns the same channel, we buffer it with 2.
	close(docker.containerWaitCh)
	docker.containerWaitCh = make(chan container.WaitResponse, 2)
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	if len(docker.containersCreated) != 2 {
		t.Fatalf("expected 2 containers created, got %d", len(docker.containersCreated))
	}
	if len(docker.containersStarted) != 2 {
		t.Fatalf("expected 2 containers started, got %d", len(docker.containersStarted))
	}
	// Both step containers should be removed
	if len(docker.containersRemoved) != 2 {
		t.Fatalf("expected 2 containers removed, got %d", len(docker.containersRemoved))
	}
}

func TestRunContainer_ContextCancellation(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	ctx, cancel := context.WithCancel(context.Background())

	// Don't send anything on containerWaitCh — simulate a long-running container.
	// Cancel the context to trigger the abort path.
	cancel()

	exitCode, err := RunContainer(ctx, docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error chain, got %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", exitCode)
	}

	// Container should have been killed
	if len(docker.containersKilled) != 1 {
		t.Fatalf("expected 1 container killed, got %d", len(docker.containersKilled))
	}

	// Cleanup should still happen
	if len(docker.networksRemoved) != 1 {
		t.Fatalf("expected network cleanup, got %d removes", len(docker.networksRemoved))
	}
}

func TestRunContainer_ArtifactsArchived(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	preserved := false
	config := scheduler.RunTaskConfig{
		ArtifactPreserver: func(reader io.Reader, t *scheduler.Task) error {
			preserved = true
			return nil
		},
	}

	_, err := RunContainer(context.Background(), docker, task, fs, config, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !preserved {
		t.Fatal("expected artifacts to be preserved")
	}
}

func TestRunContainer_ArtifactsArchivedOnStepFailure(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 1}

	preserved := false
	config := scheduler.RunTaskConfig{
		ArtifactPreserver: func(reader io.Reader, t *scheduler.Task) error {
			preserved = true
			return nil
		},
	}

	_, err := RunContainer(context.Background(), docker, task, fs, config, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Artifacts should still be archived even when a step fails
	if !preserved {
		t.Fatal("expected artifacts to be preserved even on step failure")
	}
}

func TestRunContainer_ArtifactsNotArchivedOnCancel(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	preserved := false
	config := scheduler.RunTaskConfig{
		ArtifactPreserver: func(reader io.Reader, t *scheduler.Task) error {
			preserved = true
			return nil
		},
	}

	_, _ = RunContainer(ctx, docker, task, fs, config, nil)

	// Artifacts should NOT be archived when context is cancelled
	if preserved {
		t.Fatal("expected artifacts NOT to be preserved on cancellation")
	}
}

func TestRunContainer_NetworkCreateFailure(t *testing.T) {
	docker := newMockDocker()
	docker.networkCreateErr = errors.New("network pool exhausted")
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to create docker network") {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", exitCode)
	}
}

func TestRunContainer_SecretLoading(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})
	task.Job.Job.Secrets = []*proto.Job_Env{
		{Key: "MY_SECRET", Value: "secret-name"},
	}

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	config := scheduler.RunTaskConfig{
		SecretLoader: func(name string) ([]byte, error) {
			if name == "secret-name" {
				return []byte("secret-value"), nil
			}
			return nil, fmt.Errorf("unknown secret: %s", name)
		},
	}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, config, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}

func TestRunContainer_SecretLoaderMissing(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})
	task.Job.Job.Secrets = []*proto.Job_Env{
		{Key: "MY_SECRET", Value: "secret-name"},
	}

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	// No SecretLoader configured
	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "secret loader not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", exitCode)
	}
}

func TestRunContainer_ImageOverrides(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"sha256:abc123"})

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	overrides := map[string]string{
		"sha256:abc123": "alfred-transfer:abc123",
	}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), overrides)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// The container should have been created (we can't easily check the image
	// used in the mock, but we verify it didn't error out)
	if len(docker.containersCreated) != 1 {
		t.Fatalf("expected 1 container created, got %d", len(docker.containersCreated))
	}
}

func TestRunContainer_WorkspaceCallbacks(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}

	workspaceReady := false
	workspaceTornDown := false

	config := scheduler.RunTaskConfig{
		OnWorkspaceReady: func(archiver func() (io.ReadCloser, error)) {
			workspaceReady = true
		},
		OnWorkspaceTeardown: func() {
			workspaceTornDown = true
		},
	}

	_, err := RunContainer(context.Background(), docker, task, fs, config, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !workspaceReady {
		t.Fatal("expected OnWorkspaceReady to be called")
	}
	if !workspaceTornDown {
		t.Fatal("expected OnWorkspaceTeardown to be called")
	}
}

func TestRunContainer_DockerAPIError(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	// Send an error on the errChan (Docker API failure) instead of a wait response
	docker.containerWaitErrCh <- errors.New("docker daemon crashed")

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to wait for docker container for step 1") {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", exitCode)
	}

	// Cleanup should still happen
	if len(docker.networksRemoved) != 1 {
		t.Fatalf("expected network cleanup, got %d removes", len(docker.networksRemoved))
	}
	if len(docker.containersRemoved) != 1 {
		t.Fatalf("expected container cleanup, got %d removes", len(docker.containersRemoved))
	}
}

func TestRunContainer_ContainerStartFailure(t *testing.T) {
	docker := newMockDocker()
	docker.containerStartErr = errors.New("no space left on device")
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to start docker container for step 1") {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", exitCode)
	}

	// Container was created but not started — should still be removed in cleanup
	if len(docker.containersCreated) != 1 {
		t.Fatalf("expected 1 container created, got %d", len(docker.containersCreated))
	}
	if len(docker.containersRemoved) != 1 {
		t.Fatalf("expected 1 container removed, got %d", len(docker.containersRemoved))
	}
}

func TestRunContainer_SecondStepFails(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step1:latest", "step2:latest"})

	// Buffer 2 responses: first succeeds, second fails
	docker.containerWaitCh = make(chan container.WaitResponse, 2)
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}
	docker.containerWaitCh <- container.WaitResponse{StatusCode: 5}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if exitCode != 5 {
		t.Fatalf("expected exit code 5, got %d", exitCode)
	}
	if !strings.Contains(err.Error(), "step 2 failed with status: 5") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both containers should have been created and started
	if len(docker.containersCreated) != 2 {
		t.Fatalf("expected 2 containers created, got %d", len(docker.containersCreated))
	}
	if len(docker.containersStarted) != 2 {
		t.Fatalf("expected 2 containers started, got %d", len(docker.containersStarted))
	}
	// Both step containers should be cleaned up
	if len(docker.containersRemoved) != 2 {
		t.Fatalf("expected 2 containers removed, got %d", len(docker.containersRemoved))
	}
}

func TestRunContainer_ExitCode42(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 42}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if exitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", exitCode)
	}
}

func TestRunContainer_ExitCode43(t *testing.T) {
	docker := newMockDocker()
	fs := newMockFS()
	task := testTask([]string{"step-image:latest"})

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 43}

	exitCode, err := RunContainer(context.Background(), docker, task, fs, noopRunConfig(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if exitCode != 43 {
		t.Fatalf("expected exit code 43, got %d", exitCode)
	}
}
