package scheduler

import (
	"log/slog"
	"time"
)

type Config struct {
	Logger                      *slog.Logger
	ProvisioningDelay           time.Duration
	ProvisioningFailureCooldown time.Duration
}
