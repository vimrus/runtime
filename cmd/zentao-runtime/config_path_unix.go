//go:build !windows

package main

func defaultConfigPath() string {
	return "/opt/zentao/config/runtime.json"
}
