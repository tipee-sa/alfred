package jobfile

import (
	"fmt"
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
