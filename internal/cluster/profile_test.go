package cluster

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/config"
)

func TestBuildProfileIsDeterministicAndSeparatesCapacity(t *testing.T) {
	cfg := config.DefaultForRoot("/opt/zentao")
	cfg.Cluster.ReleaseID = "release-1"
	cfg.Apps = []config.App{{
		ID:      "zentao",
		Path:    "/",
		Dir:     "/opt/zentao/apps/zentao",
		WebRoot: "www",
		DataDir: "/shared/zentao",
		TmpDir:  "/tmp/zentao",
		Enabled: true,
	}}
	storage := map[string]StorageIdentity{
		"zentao": {
			SchemaVersion: 1,
			Purpose:       "app-data",
			StorageID:     "storage-a",
			Generation:    1,
		},
	}

	first := BuildProfile(cfg, "node-a", "instance-a", "boot-a", "runtime-a", storage)
	second := BuildProfile(cfg, "node-a", "instance-a", "boot-a", "runtime-a", storage)
	if first.Runtime.Domains["cluster"] != second.Runtime.Domains["cluster"] {
		t.Fatal("profile digests must be deterministic")
	}
	if first.Apps["zentao"].StorageID != "storage-a" {
		t.Fatalf("unexpected app storage profile: %#v", first.Apps["zentao"])
	}
	if _, err := json.Marshal(first); err != nil {
		t.Fatalf("profile must be JSON serializable: %v", err)
	}

	clusterBehaviorBefore := first.Runtime.Domains["cluster"].Behavioral
	capacityBefore := first.Runtime.Domains["olap"].Capacity
	cfg.Cluster.HeartbeatInterval += time.Second
	cfg.OLAP.QueryThreads++
	changed := BuildProfile(cfg, "node-a", "instance-a", "boot-a", "runtime-a", storage)
	if changed.Runtime.Domains["cluster"].Behavioral == clusterBehaviorBefore {
		t.Fatal("cluster timing changes must be visible in the behavioral digest")
	}
	if changed.Runtime.Domains["cluster"].Critical != first.Runtime.Domains["cluster"].Critical {
		t.Fatal("cluster timing changes must not alter the critical digest")
	}
	if changed.Runtime.Domains["olap"].Capacity == capacityBefore {
		t.Fatal("capacity-only changes must be visible in the capacity digest")
	}
	if changed.Runtime.Domains["olap"].Critical != first.Runtime.Domains["olap"].Critical {
		t.Fatal("capacity-only changes must not alter the critical digest")
	}
}

func TestBuildProfileRejectsNoneButExcludesNodeLocalPaths(t *testing.T) {
	cfg := config.DefaultForRoot("/opt/zentao")
	cfg.Cluster.ReleaseID = "release-1"
	cfg.Runtime.ControlSocket = "/run/a.sock"
	cfg.Runtime.PIDFile = "/run/a.pid"
	cfg.Runtime.LogPath = "/logs/a.log"
	first := BuildProfile(cfg, "node-a", "instance-a", "boot-a", "runtime-a", nil)

	cfg.Runtime.ControlSocket = "/run/b.sock"
	cfg.Runtime.PIDFile = "/run/b.pid"
	cfg.Runtime.LogPath = "/logs/b.log"
	second := BuildProfile(cfg, "node-b", "instance-b", "boot-b", "runtime-a", nil)
	if first.Runtime.Domains["runtime"] != second.Runtime.Domains["runtime"] {
		t.Fatal("node-local paths and node identity must not change runtime digests")
	}

	now := time.Now()
	if got := ProfileAge(now, now.Add(-time.Minute)); got != time.Minute {
		t.Fatalf("profile age = %s, want 1m", got)
	}
}
