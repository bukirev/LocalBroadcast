//go:build !windows

package main

import "errors"

func openOBS(c Configuration) error {
	return errors.New("автозапуск OBS поддерживается в Windows")
}
