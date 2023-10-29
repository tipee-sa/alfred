package scheduler

import (
	"log/slog"
	"time"
)

type Config struct {
	Logger                      *slog.Logger
	ProvisioningFailureCooldown time.Duration
}
