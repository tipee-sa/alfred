package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gammadia/alfred/client/ui"
	"github.com/spf13/cobra"
)

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Update alfred client to the latest version",

	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},

	RunE: func(cmd *cobra.Command, args []string) error {
		execPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
		execPath, err = filepath.EvalSymlinks(execPath)
		if err != nil {
			return fmt.Errorf("failed to resolve executable path: %w", err)
		}

		url := fmt.Sprintf(
			"https://github.com/%s/releases/latest/download/alfred-%s-%s",
			repository, runtime.GOOS, runtime.GOARCH,
		)

		spinner := ui.NewSpinner(fmt.Sprintf("Downloading %s", url))

		resp, err := http.Get(url)
		if err != nil {
			spinner.Fail()
			return fmt.Errorf("failed to download update: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			spinner.Fail()
			return fmt.Errorf("failed to download update: HTTP %d", resp.StatusCode)
		}

		tmpFile, err := os.CreateTemp(filepath.Dir(execPath), "alfred-update-*")
		if err != nil {
			spinner.Fail()
			return fmt.Errorf("failed to create temp file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if _, err := io.Copy(tmpFile, resp.Body); err != nil {
			tmpFile.Close()
			spinner.Fail()
			return fmt.Errorf("failed to write update: %w", err)
		}
		tmpFile.Close()

		if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
			spinner.Fail()
			return fmt.Errorf("failed to make update executable: %w", err)
		}

		if err := os.Rename(tmpFile.Name(), execPath); err != nil {
			spinner.Fail()
			return fmt.Errorf("failed to replace binary: %w", err)
		}

		spinner.Success(fmt.Sprintf("Updated %s", execPath))
		return nil
	},
}
