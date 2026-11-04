package jobfile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTailBuffer_KeepsTheLastLines(t *testing.T) {
	var b tailBuffer
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	lines := strings.Split(b.String(), "\n")
	assert.Len(t, lines, tailBufferLines)
	assert.Equal(t, "line 11", lines[0])
	assert.Equal(t, "line 30", lines[len(lines)-1])
}

func TestTailBuffer_JoinsLinesSplitAcrossWrites(t *testing.T) {
	var b tailBuffer
	b.Write([]byte("ERROR: fail"))
	b.Write([]byte("ed to solve\nlast"))
	assert.Equal(t, "ERROR: failed to solve\nlast", b.String())
}

func TestTailBuffer_BoundsNewlineFreeOutput(t *testing.T) {
	var b tailBuffer
	chunk := []byte(strings.Repeat("x", 32<<10))
	for i := 0; i < 256; i++ {
		n, err := b.Write(chunk)
		assert.NoError(t, err)
		assert.Equal(t, len(chunk), n)
		assert.LessOrEqual(t, len(b.buf), tailBufferBytes)
	}
	b.Write([]byte("y"))
	out := b.String()
	assert.True(t, strings.HasPrefix(out, "[...]x"))
	assert.True(t, strings.HasSuffix(out, "xy"))
	assert.Equal(t, len("[...]")+tailBufferBytes, len(out))
}

func TestBuildCommandTurnsOffDefaultAttestations(t *testing.T) {
	t.Setenv("ALFRED_TEST_KEPT", "1")
	cmd := buildCommand("/some/dir", []string{"build", "."})
	if cmd.Dir != "/some/dir" {
		t.Errorf("Dir = %q, want /some/dir", cmd.Dir)
	}
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "\nBUILDX_NO_DEFAULT_ATTESTATIONS=1") {
		t.Error("BUILDX_NO_DEFAULT_ATTESTATIONS=1 not set: each build of an unchanged image gets a new ID")
	}
	if !strings.Contains(env, "ALFRED_TEST_KEPT=1") {
		t.Error("the user's environment is not passed to docker build")
	}
}

func TestBuildImageTagsAnImageTheDaemonKnows(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker daemon")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY file /file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte(dir), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := buildImage("Dockerfile", dir, nil, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", first).Run() })
	if out, err := exec.Command("docker", "image", "inspect", first).CombinedOutput(); err != nil {
		t.Fatalf("the daemon does not know %s: %s", first, out)
	}

	// An unchanged image keeps its ID, and the build's temporary tag is gone
	second, err := buildImage("Dockerfile", dir, nil, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, first, second, "a rebuild of an unchanged image got a new ID")
	out, _ := exec.Command("docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}", "alfred-build").Output()
	assert.Empty(t, strings.TrimSpace(string(out)), "temporary build tags left behind")
}
