package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
)

// Nil unless startUpdateCheck launched a check, so printUpdateNotice does not wait on one.
var updateCheckCh chan string

func startUpdateCheck(ctx context.Context) {
	if version == "dev" || !isatty.IsTerminal(os.Stderr.Fd()) {
		return
	}
	// Nix installs track their flake rather than GitHub releases, and self-update cannot replace them.
	if execPath, err := executablePath(); err != nil || inNixStore(execPath) {
		return
	}
	updateCheckCh = make(chan string, 1)
	go func() {
		latest, err := fetchLatestVersion(ctx)
		if err != nil || latest == "" || latest <= version {
			updateCheckCh <- ""
			return
		}
		updateCheckCh <- latest
	}()
}

func printUpdateNotice() {
	if updateCheckCh == nil {
		return
	}
	select {
	case latest := <-updateCheckCh:
		if latest != "" {
			yellow := color.New(color.FgYellow)
			yellow.EnableColor()
			blackOnYellow := color.New(color.BgYellow, color.FgBlack)
			blackOnYellow.EnableColor()

			fmt.Fprint(os.Stderr,
				yellow.Sprint("› ")+
					blackOnYellow.Sprint("Alfred update available")+
					yellow.Sprintf(" %s → %s — Run `alfred self-update` to upgrade.", version, latest)+
					"\n",
			)
		}
	case <-time.After(1 * time.Second):
	}
}

func fetchLatestVersion(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://github.com/%s/releases/latest", repository)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", err
	}

	var latest string
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			parts := strings.Split(req.URL.Path, "/")
			if len(parts) > 0 {
				latest = parts[len(parts)-1]
			}
			return http.ErrUseLastResponse
		},
	}

	resp, err := httpClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		return "", err
	}

	return latest, nil
}

func fetchTagCommit(ctx context.Context, tag string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", repository, tag)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.SHA, nil
}

// executablePath returns the path of the running binary, with symlinks resolved.
func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve executable path: %w", err)
	}
	return path, nil
}

// inNixStore reports whether path lies in the read-only Nix store.
func inNixStore(path string) bool {
	return strings.HasPrefix(path, "/nix/store/")
}

func formatVersion(ver, commitHash string) string {
	hash := truncateString(commitHash, 10)
	if hash == "" || hash == "n/a" {
		return ver
	}
	return fmt.Sprintf("%s (%s)", ver, hash)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
