package internal

import "math"

func NbNodesToCreate(maxNodes, tasksPerNode, tasksQueue, existingNodes, incomingNodes int) int {
	incomingCapacity := incomingNodes * tasksPerNode
	requiredNodes := math.Ceil(float64(tasksQueue-incomingCapacity) / float64(tasksPerNode))
	maximumMoreNodes := float64(maxNodes - existingNodes - incomingNodes)

	return int(math.Min(requiredNodes, maximumMoreNodes))
}
