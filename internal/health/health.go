// Package health derives bounded Runtime health results from lifecycle state.
package health

import (
	"time"

	"github.com/vimrus/runtime/internal/lifecycle"
)

type Report struct {
	Live      bool               `json:"live"`
	Ready     bool               `json:"ready"`
	Deep      string             `json:"deep"`
	Lifecycle lifecycle.Snapshot `json:"lifecycle"`
	CheckedAt time.Time          `json:"checkedAt"`
}

func ReportFor(snapshot lifecycle.Snapshot, deep bool) Report {
	report := Report{
		Live:      snapshot.State != lifecycle.Stopped && snapshot.State != lifecycle.Failed,
		Ready:     snapshot.State == lifecycle.Ready,
		Deep:      "not_requested",
		Lifecycle: snapshot,
		CheckedAt: time.Now().UTC(),
	}
	if deep {
		if report.Ready {
			report.Deep = "ok"
		} else {
			report.Deep = "not_ready"
		}
	}
	return report
}
