package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
)

// Spinner shows progress on stderr. When stderr is not a terminal it does not spin (that would
// write a frame every 200ms), but its final message is still written, as one plain line.
type Spinner struct {
	*spinner.Spinner
	msg  string
	done bool
}

// NewSpinner creates a new spinner with the given message.
func NewSpinner(msg string) *Spinner {
	s := &Spinner{
		Spinner: spinner.New(
			spinner.CharSets[14],
			200*time.Millisecond,
			spinner.WithHiddenCursor(true),
			spinner.WithWriterFile(os.Stderr), // spin only if stderr, where it writes, is a terminal
			spinner.WithSuffix(" "+msg),
		),
		msg: msg,
	}
	s.Start()
	return s
}

// UpdateMessage updates the spinner message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) UpdateMessage(msg string) {
	if s == nil {
		return
	}
	s.Spinner.Suffix = " " + msg
	s.msg = msg
}

// Success stops the spinner and prints a success message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Success(msg ...string) {
	s.finish(color.HiGreenString("✓"), msg)
}

// Warn stops the spinner and prints a warning message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Warn(msg ...string) {
	s.finish(color.HiYellowString("!"), msg)
}

// Fail stops the spinner and prints a failure message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Fail(msg ...string) {
	s.finish(color.HiRedString("✗"), msg)
}

// finish stops the spinner and prints its final message, once.
func (s *Spinner) finish(mark string, msg []string) {
	if s == nil || s.done {
		return
	}
	s.done = true
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	final := fmt.Sprintf("%s %s\n", mark, msg[0])
	if !s.Active() {
		// Not spinning (stderr is not a terminal), so Stop would print nothing.
		fmt.Fprint(os.Stderr, final)
		return
	}
	s.Spinner.FinalMSG = final
	s.Stop()
}
