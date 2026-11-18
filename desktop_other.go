//go:build !windows

package main

import (
	"context"
	"fmt"
)

func activateExistingDesktop() bool   { return false }
func desktopRestartArgs() []string    { return nil }
func showDesktopError(message string) {}
func (a *Application) runDesktop(ctx context.Context, show bool) error {
	if show {
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", uiPort))
	}
	<-ctx.Done()
	return nil
}
