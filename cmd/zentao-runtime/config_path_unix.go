//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

func defaultInstallRoot() string {
	executable, err := os.Executable()
	if err == nil {
		return filepath.Dir(filepath.Dir(executable))
	}
	return "/opt/zentao"
}

func defaultConfigPath() string {
	return filepath.Join(defaultInstallRoot(), "config", "runtime.toml")
}

func defaultControlSocketPath() string {
	return filepath.Join(defaultInstallRoot(), "run", "runtime.sock")
}

func defaultPIDFilePath() string {
	return filepath.Join(defaultInstallRoot(), "run", "runtime.pid")
}
