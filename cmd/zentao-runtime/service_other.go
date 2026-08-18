//go:build !windows

package main

import "fmt"

func runServiceCommand(_ []string) error {
	return fmt.Errorf("run-service is only supported on Windows")
}
