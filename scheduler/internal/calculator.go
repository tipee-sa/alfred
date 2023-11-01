package internal

import "math"

func NbNodesToCreate(maxNodes, tasksPerNode, tasksQueue, existingNodes, incomingNodes int) int {
	consideredNodes := existingNodes + incomingNodes
	incomingCapacity := consideredNodes * tasksPerNode
	requiredNodes := math.Ceil(float64(tasksQueue-incomingCapacity) / float64(tasksPerNode))
	maximumMoreNodes := float64(maxNodes - consideredNodes)

	return int(math.Max(0, math.Min(requiredNodes, maximumMoreNodes)))
}
