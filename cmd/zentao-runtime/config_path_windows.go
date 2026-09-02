//go:build windows

package main

import (
	"os"
	"path/filepath"

	"github.com/vimrus/runtime/internal/config"
)

func defaultInstallRoot() string {
	executable, err := os.Executable()
	if err == nil {
		return filepath.Dir(filepath.Dir(executable))
	}
	return `C:\Program Files\ZenTao`
}

func defaultConfigPath() string {
	return filepath.Join(defaultInstallRoot(), "config", "runtime.toml")
}

func defaultControlSocketPath() string {
	return config.Default().Runtime.ControlSocket
}

func defaultPIDFilePath() string {
	return config.Default().Runtime.PIDFile
}
