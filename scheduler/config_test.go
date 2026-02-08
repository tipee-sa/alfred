package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateDefaultTasksPerNodeMustBePositive(t *testing.T) {
	config := Config{
		MaxNodes:            1,
		DefaultTasksPerNode: 0,
	}
	err := Validate(config)
	assert.EqualError(t, err, "default-tasks-per-node must be greater than 0")
}

func TestValidateDefaultTasksPerNodeOne(t *testing.T) {
	config := Config{
		MaxNodes:            1,
		DefaultTasksPerNode: 1,
	}
	err := Validate(config)
	assert.NoError(t, err)
}

func TestValidateEmptyDefaultFlavor(t *testing.T) {
	config := Config{
		MaxNodes:            1,
		DefaultTasksPerNode: 1,
		DefaultFlavor:       "",
	}
	err := Validate(config)
	assert.NoError(t, err, "empty default flavor should be valid (used by local provisioner)")
}
