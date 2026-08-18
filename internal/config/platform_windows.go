//go:build windows

package config

func defaultControlSocket() string {
	return `\\.\pipe\zentao-runtime`
}

func defaultPIDFile() string {
	return `C:\ProgramData\ZenTao\runtime.pid`
}
