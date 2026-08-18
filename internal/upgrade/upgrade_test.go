package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutValidationRejectsOverlap(t *testing.T) {
	root := t.TempDir()
	layout := Layout{
		RuntimeDir: filepath.Join(root, "runtime"),
		AppsDir:    filepath.Join(root, "app", "releases"),
		AppCurrent: filepath.Join(root, "app", "releases", "current"),
		ConfigDir:  filepath.Join(root, "config"),
		DataDir:    filepath.Join(root, "data"),
		BackupsDir: filepath.Join(root, "backups"),
		LogsDir:    filepath.Join(root, "logs"),
	}
	if err := layout.Validate(); err == nil {
		t.Fatal("expected nested appCurrent to be rejected")
	}
}

func TestUpgradeTransactionSwitchesAndRollsBack(t *testing.T) {
	root := t.TempDir()
	layout := Layout{
		RuntimeDir: filepath.Join(root, "runtime"),
		AppsDir:    filepath.Join(root, "app", "releases"),
		AppCurrent: filepath.Join(root, "app", "current"),
		ConfigDir:  filepath.Join(root, "config"),
		DataDir:    filepath.Join(root, "data"),
		BackupsDir: filepath.Join(root, "backups"),
		LogsDir:    filepath.Join(root, "logs"),
	}
	releaseV1 := filepath.Join(layout.AppsDir, "v1")
	releaseV2 := filepath.Join(layout.AppsDir, "v2")
	for _, release := range []string{releaseV1, releaseV2} {
		if err := os.MkdirAll(filepath.Join(release, "www"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	first, err := Begin(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Stage(releaseV1); err != nil {
		t.Fatal(err)
	}
	if err := first.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := first.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	current, err := readPointer(layout.AppCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if current != releaseV1 {
		t.Fatalf("current = %q, want %q", current, releaseV1)
	}

	second, err := Begin(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Stage(releaseV2); err != nil {
		t.Fatal(err)
	}
	if err := second.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := second.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, err = readPointer(layout.AppCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if current != releaseV1 {
		t.Fatalf("after rollback current = %q, want %q", current, releaseV1)
	}
	if _, err := os.Stat(second.BackupDir()); err != nil {
		t.Fatalf("backup directory must be retained: %v", err)
	}
}

func TestUpgradeRejectsStageOutsideAppsDir(t *testing.T) {
	root := t.TempDir()
	layout := Layout{
		RuntimeDir: filepath.Join(root, "runtime"),
		AppsDir:    filepath.Join(root, "app", "releases"),
		AppCurrent: filepath.Join(root, "app", "current"),
		ConfigDir:  filepath.Join(root, "config"),
		DataDir:    filepath.Join(root, "data"),
		BackupsDir: filepath.Join(root, "backups"),
		LogsDir:    filepath.Join(root, "logs"),
	}
	transaction, err := Begin(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Stage(filepath.Join(root, "elsewhere")); err == nil {
		t.Fatal("expected release outside apps dir to be rejected")
	}
}

func TestUpgradeDoesNotOverwriteUserConfigurationOrData(t *testing.T) {
	root := t.TempDir()
	layout := Layout{
		RuntimeDir: filepath.Join(root, "runtime"),
		AppsDir:    filepath.Join(root, "app", "releases"),
		AppCurrent: filepath.Join(root, "app", "current"),
		ConfigDir:  filepath.Join(root, "config"),
		DataDir:    filepath.Join(root, "data"),
		BackupsDir: filepath.Join(root, "backups"),
		LogsDir:    filepath.Join(root, "logs"),
	}
	releaseV1 := filepath.Join(layout.AppsDir, "v1")
	releaseV2 := filepath.Join(layout.AppsDir, "v2")
	for _, release := range []string{releaseV1, releaseV2} {
		if err := os.MkdirAll(filepath.Join(release, "www"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(layout.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userConfig := filepath.Join(layout.ConfigDir, "php.ini")
	if err := os.WriteFile(userConfig, []byte("; user config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(layout.DataDir, "attachment.bin")
	if err := os.MkdirAll(layout.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	transaction, err := Begin(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Stage(releaseV1); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	configAfter, err := os.ReadFile(userConfig)
	if err != nil || string(configAfter) != "; user config\n" {
		t.Fatalf("user config was overwritten: %q %v", configAfter, err)
	}
	dataAfter, err := os.ReadFile(dataFile)
	if err != nil || string(dataAfter) != "data" {
		t.Fatalf("data was overwritten: %q %v", dataAfter, err)
	}
}
