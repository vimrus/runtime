package logging

// JSONLFileName returns the per-node JSONL file name. Single-node deployments
// intentionally omit the node suffix when nodeID is empty.
func JSONLFileName(prefix, nodeID string) string {
	if nodeID == "" {
		return prefix + ".jsonl"
	}
	return prefix + "-" + nodeID + ".jsonl"
}
