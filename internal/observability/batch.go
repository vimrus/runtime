package observability

import (
	"fmt"
	"path/filepath"
	"time"
)

// Partition is the Hive-style target directory for one immutable Parquet
// part file.
type Partition struct {
	Date   string
	Hour   string
	NodeID string
}

func PartitionFor(eventTime time.Time, nodeID string) Partition {
	eventTime = eventTime.UTC()
	return Partition{
		Date:   eventTime.Format("2006-01-02"),
		Hour:   eventTime.Format("15"),
		NodeID: nodeID,
	}
}

func (p Partition) Directory(datasetRoot string, kind Kind) string {
	return filepath.Join(datasetRoot, string(kind), "schema=v1", "date="+p.Date, "hour="+p.Hour, "node="+p.NodeID)
}

func (p Partition) FinalPath(datasetRoot string, kind Kind, batchID string) string {
	return filepath.Join(p.Directory(datasetRoot, kind), "part-"+batchID+".parquet")
}

func (p Partition) TempPath(datasetRoot string, kind Kind, batchID string) string {
	return filepath.Join(p.Directory(datasetRoot, kind), "."+batchID+".parquet.tmp")
}

// BatchID is the idempotent commit key: nodeID + bootID + monotonic sequence.
func BatchID(nodeID, bootID string, sequence uint64) string {
	return fmt.Sprintf("%s-%s-%020d", nodeID, bootID, sequence)
}

// BatchRecord is the persisted spool manifest used for crash recovery.
type BatchRecord struct {
	BatchID   string    `json:"batchID"`
	Kind      Kind      `json:"kind"`
	Partition Partition `json:"partition"`
	Rows      int       `json:"rows"`
	FinalPath string    `json:"finalPath"`
	SpoolPath string    `json:"spoolPath"`
	SealedAt  time.Time `json:"sealedAt"`
}
