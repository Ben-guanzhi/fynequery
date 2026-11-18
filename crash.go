package main

// Crash capture: any unhandled panic (main goroutine, fyne.Do callbacks or
// background workers) is written to crash.log next to the config dir, so a
// silent exit can be diagnosed. dist builds run without a console, so the
// log file is the only reliable post-mortem.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"fyne.io/fyne/v2"
)

// writeCrash dumps the panic and a full stack trace to stderr + crash.log.
func writeCrash(where string, r interface{}) {
	buf := make([]byte, 64*1024)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf("=== %s panic: %v\n%s\n", where, r, buf[:n])
	os.Stderr.WriteString(msg)
	// crash.log 也带上限：崩溃路径没有轮转器，无上限的 append 会占满磁盘。
	// 超限时截断后写（保留最后一次崩溃的全栈——最近的现场最有诊断价值）。
	const maxCrashLogSize = 1 << 20 // 1 MB
	crashFlag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(filepath.Join(configDir(), "crash.log")); err == nil && info.Size() > maxCrashLogSize {
		crashFlag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	if f, err := os.OpenFile(filepath.Join(configDir(), "crash.log"),
		crashFlag, 0o600); err == nil {
		f.WriteString(msg)
		f.Close()
	}
}

// safeDo is fyne.Do with panic recovery: a panicking UI callback is logged
// instead of taking the whole app down.
func safeDo(fn func()) {
	fyne.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				writeCrash("fyne.Do callback", r)
			}
		}()
		fn()
	})
}

// safeGo runs fn in a background goroutine with panic recovery, keeping the
// app alive and recording the failure for post-mortem.
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				writeCrash("worker "+name, r)
			}
		}()
		fn()
	}()
}
