//go:build !windows

package db

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
