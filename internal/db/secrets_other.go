//go:build !windows

package db

// Without DPAPI there is no local key to hide a password behind, so non-Windows
// builds keep the legacy plain-text store instead of pretending to encrypt.
// Hardening this properly needs the platform keychain (macOS) or libsecret over
// D-Bus (Linux); neither can be exercised from the Windows development box, so
// the store stays honest about what it is and the connection dialog warns.
func protectSecret(plaintext string) (string, error) {
	return plaintext, nil
}

func unprotectSecret(stored string) (string, error) {
	return stored, nil
}

// SecretsSealed reports that this platform cannot seal a saved password: the
// connection dialog shows the user that the password file holds it in clear
// text, protected only by the file's 0600 permission.
func SecretsSealed() bool { return false }
