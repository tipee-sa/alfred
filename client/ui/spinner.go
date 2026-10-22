package ui

import (
	"fmt"
	"io"
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
	w    io.Writer // where the final message goes when not spinning
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
		w:   os.Stderr,
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
	s.finish("✓", color.FgHiGreen, msg)
}

// Warn stops the spinner and prints a warning message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Warn(msg ...string) {
	s.finish("!", color.FgHiYellow, msg)
}

// Fail stops the spinner and prints a failure message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Fail(msg ...string) {
	s.finish("✗", color.FgHiRed, msg)
}

// finish stops the spinner and prints its final message, once. The mark is coloured only while
// spinning: fatih/color decides from stdout, which can be a terminal when stderr is a log file.
func (s *Spinner) finish(mark string, markColor color.Attribute, msg []string) {
	if s == nil || s.done {
		return
	}
	s.done = true
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	if !s.Active() {
		// Not spinning (stderr is not a terminal), so Stop would print nothing.
		fmt.Fprintf(s.w, "%s %s\n", mark, msg[0])
		return
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.New(markColor).Sprint(mark), msg[0])
	s.Stop()
}
