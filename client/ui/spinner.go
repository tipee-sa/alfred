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

func (s *Spinner) UpdateMessage(msg string) {
	s.Spinner.Suffix = " " + msg
	s.msg = msg
}

func (s *Spinner) Success(msg ...string) {
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.HiGreenString("✓"), msg[0])
	s.Stop()
}

func (s *Spinner) Warn(msg ...string) {
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.HiYellowString("!"), msg[0])
	s.Stop()
}

func (s *Spinner) Fail(msg ...string) {
	if len(msg) == 0 {
		msg = []string{s.msg}
	}
	s.Spinner.FinalMSG = fmt.Sprintf("%s %s\n", color.HiRedString("✗"), msg[0])
	s.Stop()
}
