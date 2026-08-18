//go:build !windows

package config

func defaultControlSocket() string {
	return "/run/zentao/runtime.sock"
}

func defaultPIDFile() string {
	return "/run/zentao/runtime.pid"
}
