package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
)

type Spinner struct {
	*spinner.Spinner
	msg string
}

// NewSpinner creates a new spinner with the given message.
func NewSpinner(msg string) *Spinner {
	s := &Spinner{
		spinner.New(
			spinner.CharSets[14],
			200*time.Millisecond,
			spinner.WithHiddenCursor(true),
			spinner.WithWriter(os.Stderr),
			spinner.WithSuffix(" "+msg),
		),
		msg,
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
	if s == nil {
		return
	}
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.HiGreenString("✓"), msg[0])
	s.Stop()
}

// Warn stops the spinner and prints a warning message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Warn(msg ...string) {
	if s == nil {
		return
	}
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.HiYellowString("!"), msg[0])
	s.Stop()
}

// Fail stops the spinner and prints a failure message.
// This function is safe to call on a nil Spinner.
func (s *Spinner) Fail(msg ...string) {
	if s == nil {
		return
	}
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.HiRedString("✗"), msg[0])
	s.Stop()
}
