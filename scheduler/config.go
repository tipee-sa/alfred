package scheduler

import (
	"fmt"
	"io"
	"log/slog"
	"time"
)

type ArtifactPreserver func(io.Reader, *Task) error
type SecretLoader func(string) ([]byte, error)

type Config struct {
	ArtifactPreserver           ArtifactPreserver `json:"-"`
	SlotsPerNode                int               `json:"slots-per-node"`
	Logger                      *slog.Logger      `json:"-"`
	MaxNodes                    int               `json:"max-nodes"`
	ProvisioningDelay           time.Duration     `json:"provisioning-delay"`
	ProvisioningFailureCooldown time.Duration     `json:"provisioning-failure-cooldown"`
	SecretLoader                SecretLoader      `json:"-"`
	TaskStartupDelay            time.Duration     `json:"task-startup-delay"`
	TaskTimeout                 time.Duration     `json:"task-timeout"`
}

func Validate(config Config) error {
	if config.MaxNodes < 1 {
		return fmt.Errorf("max-nodes must be greater than 0")
	}
	if config.SlotsPerNode < 1 {
		return fmt.Errorf("slots-per-node must be greater than 0")
	}
	if config.ProvisioningDelay < 0 {
		return fmt.Errorf("provisioning-delay must be greater than or equal to 0")
	}
	if config.ProvisioningFailureCooldown < 0 {
		return fmt.Errorf("provisioning-failure-cooldown must be greater than or equal to 0")
	}

	return nil
}
