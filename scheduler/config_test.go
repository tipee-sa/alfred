package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateSlotsPerNodeMustBePositive(t *testing.T) {
	config := Config{
		MaxNodes:     1,
		SlotsPerNode: 0,
	}
	err := Validate(config)
	assert.EqualError(t, err, "slots-per-node must be greater than 0")
}

func TestValidateSlotsPerNodeOne(t *testing.T) {
	config := Config{
		MaxNodes:     1,
		SlotsPerNode: 1,
	}
	err := Validate(config)
	assert.NoError(t, err)
}
