//go:build !windows

package main

import "github.com/vimrus/runtime/internal/control"

func controlAuthorizer() control.Authorizer {
	return control.SameUserAuthorizer{}
}
