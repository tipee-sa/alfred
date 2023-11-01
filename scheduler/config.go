package scheduler

import (
	"io"
	"log/slog"
	"time"
)

type ArtifactPreserver func(io.Reader, *Task) error

type Config struct {
	ArtifactPreserver           ArtifactPreserver `json:"-"`
	Logger                      *slog.Logger      `json:"-"`
	MaxNodes                    int               `json:"max-nodes"`
	ProvisioningDelay           time.Duration     `json:"provisioning-delay"`
	ProvisioningFailureCooldown time.Duration     `json:"provisioning-failure-cooldown"`
	TasksPerNode                int               `json:"tasks-per-node"`
}
