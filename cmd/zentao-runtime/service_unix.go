//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func installServiceCommand(args []string) error {
	name, start, err := serviceNameFlag("service install", args)
	if err != nil {
		return usageError("%v", err)
	}
	if name == "" {
		return usageError("service name must not be empty")
	}
	root := defaultInstallRoot()
	unit := fmt.Sprintf(`[Unit]
Description=ZenTao Runtime
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=zentao
Group=zentao
ExecStart=%s/bin/zentao serve
ExecReload=%s/bin/zentao reload --control-socket /run/zentao/runtime.sock
Restart=on-failure
RestartSec=2
TimeoutStopSec=45
KillMode=control-group
LimitNOFILE=65536
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s/data %s/config %s/logs %s/spool %s/observability /run/zentao

[Install]
WantedBy=multi-user.target
`, root, root, filepath.Join(root, "data"), filepath.Join(root, "config"), filepath.Join(root, "logs"), filepath.Join(root, "spool"), filepath.Join(root, "observability"))
	unitPath := filepath.Join("/etc/systemd/system", name+".service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl("enable", name+".service"); err != nil {
		return err
	}
	if start {
		if err := runSystemctl("start", name+".service"); err != nil {
			return err
		}
	}
	return nil
}

func uninstallServiceCommand(args []string) error {
	name, _, err := serviceNameFlag("service uninstall", args)
	if err != nil {
		return usageError("%v", err)
	}
	if err := runSystemctl("disable", "--now", name+".service"); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join("/etc/systemd/system", name+".service")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	return runSystemctl("daemon-reload")
}

func runSystemctl(args ...string) error {
	command := exec.Command("systemctl", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", args[0], err)
	}
	return nil
}
