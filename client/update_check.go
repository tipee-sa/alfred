package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
)

var updateCheckCh = make(chan string, 1)

func startUpdateCheck(ctx context.Context) {
	if version == "dev" || !isatty.IsTerminal(os.Stderr.Fd()) {
		return
	}
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
