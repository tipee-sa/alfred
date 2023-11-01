package internal

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	step1 = 1
	step2 = 2
	step3 = 3
)

var flagtests = map[int]struct {
	maxNodes                int
	tasksPerNode            int
	tasksQueue              int
	runningNodes            int
	provisioningNodes       int
	provisioningQueuedNodes int
	queuedNodes             int
	expectations            map[int]int
}{
	0: {
		1, 1, // max nodes, tasks per node
		0, 0, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 0, step2: 0, step3: 0},
	},
	1: {
		1, 1, // max nodes, tasks per node
		1, 0, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 1, step2: 1, step3: 1},
	},
	2: {
		1, 1, // max nodes, tasks per node
		5, 0, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 1, step2: 1, step3: 1},
	},
	3: {
		3, 1, // max nodes, tasks per node
		5, 0, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 3, step2: 3, step3: 3},
	},
	4: {
		5, 1, // max nodes, tasks per node
		3, 0, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 3, step2: 3, step3: 3},
	},
	5: {
		5, 1, // max nodes, tasks per node
		3, 1, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 2, step2: 2, step3: 2},
	},
	6: {
		4, 4, // max nodes, tasks per node
		20, 0, // tasks, running nodes
		0, 0, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 4, step2: 4, step3: 4},
	},
	7: {
		4, 4, // max nodes, tasks per node
		16, 1, // tasks, running nodes
		0, 0, 3, // provisioning, queue ready, queue future
		map[int]int{step1: 3, step2: 3, step3: 0},
	},
	8: {
		4, 4, // max nodes, tasks per node
		16, 1, // tasks, running nodes
		0, 1, 2, // provisioning, queue ready, queue future
		map[int]int{step1: 3, step2: 2, step3: 0},
	},
	9: {
		4, 4, // max nodes, tasks per node
		16, 1, // tasks, running nodes
		0, 1, 2, // provisioning, queue ready, queue future
		map[int]int{step1: 3, step2: 2, step3: 0},
	},
	10: {
		4, 4, // max nodes, tasks per node
		12, 1, // tasks, running nodes
		0, 0, 3, // provisioning, queue ready, queue future
		map[int]int{step1: 2, step2: 2, step3: 0},
	},
	11: {
		4, 4, // max nodes, tasks per node
		12, 1, // tasks, running nodes
		0, 1, 2, // provisioning, queue ready, queue future
		map[int]int{step1: 2, step2: 1, step3: 0},
	},
	12: {
		4, 4, // max nodes, tasks per node
		8, 1, // tasks, running nodes
		0, 1, 2, // provisioning, queue ready, queue future
		map[int]int{step1: 1, step2: 0, step3: 0},
	},
	13: {
		4, 4, // max nodes, tasks per node
		8, 1, // tasks, running nodes
		1, 1, 1, // provisioning, queue ready, queue future
		map[int]int{step1: 0, step2: 0, step3: 0},
	},
	14: {
		4, 4, // max nodes, tasks per node
		8, 2, // tasks, running nodes
		1, 0, 1, // provisioning, queue ready, queue future
		map[int]int{step1: 0, step2: 0, step3: 0},
	},
	15: {
		4, 4, // max nodes, tasks per node
		4, 2, // tasks, running nodes
		1, 0, 1, // provisioning, queue ready, queue future
		map[int]int{step1: 0, step2: 0, step3: 0},
	},
	16: {
		4, 4, // max nodes, tasks per node
		0, 2, // tasks, running nodes
		1, 1, 0, // provisioning, queue ready, queue future
		map[int]int{step1: 0, step2: 0, step3: 0},
	},
}

func TestNbNodesToCreate(t *testing.T) {
	var incomingNodes int
	for index, tt := range flagtests {
		for step, expected := range tt.expectations {
			t.Run(fmt.Sprintf("test-%d-expectation-%d", index, step), func(t *testing.T) {
				switch step {
				case step1:
					incomingNodes = tt.provisioningNodes
				case step2:
					incomingNodes = tt.provisioningNodes + tt.provisioningQueuedNodes
				case step3:
					incomingNodes = tt.provisioningNodes + tt.provisioningQueuedNodes + tt.queuedNodes
				}
				assert.Equal(t, expected, NbNodesToCreate(tt.maxNodes, tt.tasksPerNode, tt.tasksQueue, tt.runningNodes, incomingNodes))
			})
		}
	}
}
