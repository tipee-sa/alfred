package scheduler

import (
	"log/slog"
	"time"
)

type Config struct {
	Logger                      *slog.Logger  `json:"-"`
	MaxNodes                    int           `json:"max-nodes"`
	MaxTasksPerNode             int           `json:"max-tasks-per-node"`
	ProvisioningDelay           time.Duration `json:"provisioning-delay"`
	ProvisioningFailureCooldown time.Duration `json:"provisioning-failure-cooldown"`
}
