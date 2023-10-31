package scheduler

import (
	"log/slog"
	"time"
)

type Config struct {
	Logger                      *slog.Logger  `json:"-"`
	ProvisioningDelay           time.Duration `json:"provisioning-delay"`
	ProvisioningFailureCooldown time.Duration `json:"provisioning-failure-cooldown"`
}
