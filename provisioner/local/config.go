package local

import (
	"log/slog"
)

type Config struct {
	// Logger to use
	Logger *slog.Logger `json:"-"`
	// Maximum number of nodes that can be provisioned
	MaxNodes int `json:"provisioner-max-nodes"`
	// Maximum number of tasks that can be run on a single node
	MaxTasksPerNode int `json:"provisioner-max-tasks-per-node"`
}
