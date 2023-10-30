package local

import (
	"log/slog"
)

type Config struct {
	// Logger to use
	Logger *slog.Logger
	// Maximum number of nodes that can be provisioned
	MaxNodes int
	// Maximum number of tasks that can be run on a single node
	MaxTasksPerNode int
}
