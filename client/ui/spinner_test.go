package ui

import (
	"bytes"
	"testing"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
	"github.com/stretchr/testify/assert"
)

func TestSpinner_NotSpinningWritesPlainFinalLine(t *testing.T) {
	// stdout a terminal (colours on) and stderr redirected, as in `alfred run job.yaml 2>job.log`
	defer func(noColor bool) { color.NoColor = noColor }(color.NoColor)
	color.NoColor = false

	for want, finish := range map[string]func(*Spinner){
		"✓ Building\n": func(s *Spinner) { s.Success() },
		"! Building\n": func(s *Spinner) { s.Warn() },
		"✗ Failed\n":   func(s *Spinner) { s.Fail("Failed") },
	} {
		var buf bytes.Buffer
		s := &Spinner{Spinner: spinner.New(spinner.CharSets[14], time.Second), msg: "Building", w: &buf} // never started
		finish(s)
		finish(s)

		assert.Equal(t, want, buf.String(), "one line, without escape codes")
	}
}
