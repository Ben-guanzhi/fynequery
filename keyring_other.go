//go:build !windows

package main

// Non-Windows platforms: no OS keyring integration yet, so all keyring
// operations are no-ops and the connection falls back to plaintext storage.

func keyringAvailable() bool { return false }

func keyringSet(_, _ string) error { return nil }

func keyringGet(name string) (string, bool) { return "", false }

func keyringDelete(string) error { return nil }
