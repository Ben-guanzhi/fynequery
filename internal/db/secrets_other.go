//go:build !windows

package db

// Without DPAPI there is no local key to hide a password behind, so non-Windows
// builds keep the legacy plain-text store instead of pretending to encrypt.
func protectSecret(plaintext string) (string, error) {
	return plaintext, nil
}

func unprotectSecret(stored string) (string, error) {
	return stored, nil
}
