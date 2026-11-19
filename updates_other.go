//go:build !windows

package main

import (
	"fmt"
	"time"
)

type exactUpdateParent struct{}

func updateProcessCreated(int) (uint64, error) {
	return 0, fmt.Errorf("обновление доступно только в Windows x64")
}
func openExactUpdateParent(int, uint64) (*exactUpdateParent, error) {
	return nil, fmt.Errorf("Windows required")
}
func (*exactUpdateParent) close()                           {}
func (*exactUpdateParent) wait(time.Duration) error         { return fmt.Errorf("Windows required") }
func lockUpdateInstall(string) (func(), error)              { return nil, fmt.Errorf("Windows required") }
func checkUpdateSpace(string, uint64) error                 { return fmt.Errorf("Windows required") }
func updateConfirm(string) bool                             { return false }
func runUpdateProgress(work func(func(string)) error) error { return work(func(string) {}) }

func showUpdateNotice(string) {}

func configureUpdateDesktopInstance(int) {}

func updateOBSRunning() (bool, error) { return false, fmt.Errorf("Windows required") }
