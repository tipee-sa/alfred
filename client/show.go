package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show [JOB]",
	Short: "Show job details",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		job, err := getJob(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		cmd.Printf("%-12s %s\n", "Job:", color.HiCyanString(job.Name))
		if job.StartedBy != "" {
			cmd.Printf("%-12s %s\n", "Started by:", job.StartedBy)
		}
		cmd.Printf("%-12s %s\n", "Scheduled:", job.ScheduledAt.AsTime().Truncate(time.Second))
		if job.CommandLine != "" {
			cmd.Println("Command:")
			cmd.Println(formatCommandLine(job.CommandLine, 80))
		}

		if job.Jobfile != "" {
			cmd.Println()
			cmd.Println(fmt.Sprintf("--- %s ---", color.HiWhiteString("Jobfile")))
			cmd.Print(stripJobfileNoise(job.Jobfile))
		}

		return nil
	},
}

// formatCommandLine wraps a shell-quoted command string across multiple lines for
// readability, using backslash continuations so the output is copy-pasteable into a shell.
// It forces line breaks before each "-p" flag (keeping -p and its value together)
// and before the first positional argument after the -p flags.
func formatCommandLine(command string, maxWidth int) string {
	args := shellFields(command)
	if len(args) == 0 {
		return ""
	}

	const indent = "  "
	const continuation = indent + "  "

	var lines []string
	line := indent + args[0]
	lastWasParamFlag := false
	hadParamFlags := false
	instanceBreakDone := false

	for _, arg := range args[1:] {
		// Force break before every -p flag
		if arg == "-p" {
			lines = append(lines, line+" \\")
			line = continuation + arg
			lastWasParamFlag = true
			hadParamFlags = true
			continue
		}

		// Keep -p value on the same line as -p
		if lastWasParamFlag {
			line += " " + arg
			lastWasParamFlag = false
			continue
		}

		// Force break before the first positional arg after -p flags
		if hadParamFlags && !instanceBreakDone && !strings.HasPrefix(arg, "-") {
			lines = append(lines, line+" \\")
			line = continuation + arg
			instanceBreakDone = true
			continue
		}

		// Normal maxWidth wrapping
		if len(line)+1+len(arg)+2 > maxWidth {
			lines = append(lines, line+" \\")
			line = continuation + arg
		} else {
			line += " " + arg
		}
	}
	lines = append(lines, line)

	return strings.Join(lines, "\n")
}

// stripJobfileNoise removes comment lines and collapses consecutive blank lines
// from the rendered jobfile, producing a cleaner display output.
func stripJobfileNoise(source string) string {
	lines := strings.Split(source, "\n")
	var result []string
	lastBlank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "" {
			if lastBlank {
				continue
			}
			lastBlank = true
		} else {
			lastBlank = false
		}
		result = append(result, line)
	}
	// Trim leading/trailing blank lines
	for len(result) > 0 && strings.TrimSpace(result[0]) == "" {
		result = result[1:]
	}
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}
	if len(result) == 0 {
		return ""
	}
	return strings.Join(result, "\n") + "\n"
}

// shellFields splits a shell-quoted string into tokens, respecting single quotes
// (as produced by shellescape.QuoteCommand). Unquoted tokens are split on whitespace.
func shellFields(s string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	hasContent := false // tracks whether we've seen any content (including empty quotes)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inQuote:
			inQuote = true
			hasContent = true
		case ch == '\'' && inQuote:
			inQuote = false
		case ch == ' ' && !inQuote:
			if hasContent {
				args = append(args, current.String())
				current.Reset()
				hasContent = false
			}
		default:
			current.WriteByte(ch)
			hasContent = true
		}
	}
	if hasContent {
		args = append(args, current.String())
	}
	return args
}
