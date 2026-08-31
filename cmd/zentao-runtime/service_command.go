package main

import (
	"errors"
	"flag"
)

func serviceCommand(args []string) error {
	if len(args) == 0 {
		return usageError("usage: zentao service <install|uninstall>")
	}
	switch args[0] {
	case "install":
		return installServiceCommand(args[1:])
	case "uninstall":
		return uninstallServiceCommand(args[1:])
	default:
		return usageError("unknown service operation %q", args[0])
	}
}

func serviceNameFlag(command string, args []string) (string, bool, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	name := flags.String("name", "ZenTaoRuntime", "Service name")
	start := flags.Bool("start", true, "Start the service after installation")
	if err := flags.Parse(args); err != nil {
		return "", false, err
	}
	if flags.NArg() != 0 {
		return "", false, errors.New("unexpected arguments")
	}
	return *name, *start, nil
}
