//go:build windows

package main

import (
	"context"
	"flag"
	"time"

	"github.com/vimrus/runtime/internal/control"
	"github.com/vimrus/runtime/internal/platform/windows"
)

func runServiceCommand(args []string) error {
	flags := flag.NewFlagSet("run-service", flag.ContinueOnError)
	name := flags.String("name", "ZenTaoRuntime", "Windows service name")
	configPath := flags.String("config", `C:\Program Files\ZenTao\config\runtime.json`, "Runtime configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	serveArgs := []string{"--config", *configPath}
	serveArgs = append(serveArgs, flags.Args()...)
	errc := make(chan error, 1)
	go func() { errc <- serve(serveArgs) }()
	return windows.Run(windows.Service{
		Name:        *name,
		Start:       func(context.Context) error { return nil },
		StopTimeout: 45 * time.Second,
		Stop: func(ctx context.Context) error {
			response, err := control.Call(ctx, `\\.\pipe\zentao-runtime`, control.Request{Operation: "stop"})
			if err == nil && !response.OK && response.Error != nil {
				return errFromControl(response)
			}
			if err != nil {
				return err
			}
			select {
			case serveErr := <-errc:
				return serveErr
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}

func errFromControl(response control.Response) error {
	return &exitError{code: 1, message: response.Error.Code + ": " + response.Error.Message}
}
