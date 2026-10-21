//go:build windows

package db

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW: spawn the plugin without a flashing console window.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
