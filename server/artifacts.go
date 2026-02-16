package main

import (
	"os"
	"path"
	"time"

	"github.com/gammadia/alfred/server/log"
)

func cleanupOldArtifacts(dataRoot string, retention time.Duration) {
	artifactsDir := path.Join(dataRoot, "artifacts")

	entries, err := os.ReadDir(artifactsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Warn("Failed to read artifacts directory", "error", err)
		return
	}

	cutoff := time.Now().Add(-retention)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Warn("Failed to stat artifact directory", "name", entry.Name(), "error", err)
			continue
		}

		if info.ModTime().Before(cutoff) {
			age := time.Since(info.ModTime()).Truncate(24 * time.Hour)
			if err := os.RemoveAll(path.Join(artifactsDir, entry.Name())); err != nil {
				log.Warn("Failed to delete old artifact directory", "name", entry.Name(), "error", err)
				continue
			}
			log.Info("Deleted old artifact directory", "name", entry.Name(), "age", age)
		}
	}
}
