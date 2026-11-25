package internal

import "math"

// NbNodesToCreate tells how many more nodes the queued tasks need, within maxNodes; a negative
// result is a number of incoming nodes to give up. incomingNodes are the nodes whose slots are
// still to come (provisioning or queued), existingNodes all the others (online, terminating,
// failed): a node counts in one of them, never in both.
func NbNodesToCreate(maxNodes, slotsPerNode, totalSlotsNeeded, existingNodes, incomingNodes int) int {
	incomingCapacity := incomingNodes * slotsPerNode
	requiredNodes := math.Ceil(float64(totalSlotsNeeded-incomingCapacity) / float64(slotsPerNode))
	maximumMoreNodes := float64(maxNodes - existingNodes - incomingNodes)

	return int(math.Min(requiredNodes, maximumMoreNodes))
}
