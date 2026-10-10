//go:build !windows

package db

import (
	"fmt"
	"sync"
)

// protectSecret stores the password in the OS keyring (macOS Keychain, Linux
// Secret Service). A machine with no usable keyring falls back to the legacy
// plain-text store; the fallback and its warning live in protectViaKeyringOrPlain.
func protectSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return protectViaKeyringOrPlain(plaintext)
}

// unsealDPAPI refuses a Windows-sealed value: the key that opens it lives in
// another machine's DPAPI, so the password has to be retyped here.
func unsealDPAPI(stored string) (string, error) {
	return "", fmt.Errorf("password was sealed with Windows DPAPI; retype it")
}

var (
	secretsOnce sync.Once
	secretsOK   bool
)

// SecretsSealed reports whether a saved password is stored sealed rather than
// in clear text. Off Windows that cannot be known at build time — it depends
// on whether this machine has a reachable keyring — so the answer comes from a
// one-time probe: keyring present, secrets are sealed there; keyring absent,
// the legacy plain-text store is in use and the dialog says so.
func SecretsSealed() bool {
	secretsOnce.Do(func() { secretsOK = keyringWorks() })
	return secretsOK
}
