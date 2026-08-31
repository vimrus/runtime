//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/svc/mgr"
)

func installServiceCommand(args []string) error {
	name, start, err := serviceNameFlag("service install", args)
	if err != nil {
		return usageError("%v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Runtime executable: %w", err)
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.CreateService(name, executable, mgr.Config{
		DisplayName: name,
		StartType:   mgr.StartAutomatic,
		Description: "ZenTao Runtime",
	}, "run-service", "--name", name)
	if err != nil {
		return fmt.Errorf("create Windows service: %w", err)
	}
	defer service.Close()
	if start {
		if err := service.Start(); err != nil {
			return fmt.Errorf("start Windows service: %w", err)
		}
	}
	return nil
}

func uninstallServiceCommand(args []string) error {
	name, _, err := serviceNameFlag("service uninstall", args)
	if err != nil {
		return usageError("%v", err)
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		return fmt.Errorf("open Windows service: %w", err)
	}
	defer service.Close()
	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete Windows service: %w", err)
	}
	return nil
}
