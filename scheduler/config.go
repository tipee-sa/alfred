package scheduler

import "time"

type Config struct {
	ProvisioningFailureCooldown time.Duration
}
