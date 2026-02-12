package internal

import "math"

func NbNodesToCreate(maxNodes, slotsPerNode, totalSlotsNeeded, existingNodes, incomingNodes int) int {
	incomingCapacity := incomingNodes * slotsPerNode
	requiredNodes := math.Ceil(float64(totalSlotsNeeded-incomingCapacity) / float64(slotsPerNode))
	maximumMoreNodes := float64(maxNodes - existingNodes - incomingNodes)

	return int(math.Min(requiredNodes, maximumMoreNodes))
}
