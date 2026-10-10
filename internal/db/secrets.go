package db

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/zalando/go-keyring"
)

// File-form prefixes for a saved password. Which one a value carries says how
// it was sealed and where the key lives; a value with neither prefix is legacy
// plain text and loads as-is.
const (
	secretPrefix  = "dpapi:"   // sealed with Windows DPAPI (key derived by the OS)
	keyringPrefix = "keyring:" // stored in the OS keyring; the value is a keyring id
)

// keyringService is the OS keyring namespace both this client and the parent
// Wails client write to, so a password saved by either opens in the other. It
// has to stay in lockstep with TableProGo/secrets.go.
const keyringService = "TableProGo"

// newSecretID returns a fresh random id for a keyring entry. Sealed values are
// keyed by this id, not by the connection id: connection ids are minted
// per-client and can be reused after a delete, while an id minted once per
// seal is stable for as long as the file references it.
func newSecretID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate secret id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// sealViaKeyring stores plaintext in the OS keyring under a fresh id and
// returns the file-form reference "keyring:<id>".
func sealViaKeyring(plaintext string) (string, error) {
	id, err := newSecretID()
	if err != nil {
		return "", err
	}
	if err := keyring.Set(keyringService, id, plaintext); err != nil {
		return "", fmt.Errorf("keyring set: %w", err)
	}
	return keyringPrefix + id, nil
}

// openViaKeyring fetches the password a "keyring:<id>" reference points at.
func openViaKeyring(id string) (string, error) {
	plaintext, err := keyring.Get(keyringService, id)
	if err != nil {
		return "", fmt.Errorf("keyring get: %w", err)
	}
	return plaintext, nil
}

// protectViaKeyringOrPlain seals into the OS keyring, falling back to the
// legacy plain-text store when the machine has no usable keyring (no Secret
// Service on Linux, keychain denied, ...). Refusing to save would lose the
// connection; storing clear text is the documented lesser evil.
func protectViaKeyringOrPlain(plaintext string) (string, error) {
	sealed, err := sealViaKeyring(plaintext)
	if err != nil {
		log.Printf("keyring unavailable, storing password in plain text: %v", err)
		return plaintext, nil
	}
	return sealed, nil
}

// unprotectSecret opens a saved password by its file-form prefix: keyring
// references are fetched from the OS keyring, dpapi values through the
// platform's DPAPI (which refuses off Windows), and legacy plain text passes
// through so it gets sealed on the next save.
func unprotectSecret(stored string) (string, error) {
	switch {
	case strings.HasPrefix(stored, keyringPrefix):
		return openViaKeyring(strings.TrimPrefix(stored, keyringPrefix))
	case strings.HasPrefix(stored, secretPrefix):
		return unsealDPAPI(stored)
	default:
		return stored, nil
	}
}

// isSealedValue reports whether a saved password is already in a sealed form:
// load must not treat it as needing migration, and every seal form this code
// writes counts, not just the one the current platform produces.
func isSealedValue(v string) bool {
	return strings.HasPrefix(v, secretPrefix) || strings.HasPrefix(v, keyringPrefix)
}

// keyringWorks probes the OS keyring once by storing and removing a throwaway
// entry. A machine without a reachable keyring (headless Linux without Secret
// Service, denied keychain) answers false, which is exactly the condition
// under which protectSecret falls back to plain text.
func keyringWorks() bool {
	const probeUser = "__tablepro_probe__"
	if err := keyring.Set(keyringService, probeUser, "probe"); err != nil {
		return false
	}
	_ = keyring.Delete(keyringService, probeUser)
	return true
}

// secretsGC removes keyring entries the previous save referenced that this
// save dropped: a deleted connection, a changed password, a cleared one.
// Best-effort — a failed delete leaks one keyring entry but never breaks the
// save, and a reference this process never saw on disk is left alone in case
// the other client still holds it.
func secretsGC(oldVals, newVals []string) {
	keep := keyringRefSet(newVals)
	for id := range keyringRefSet(oldVals) {
		if keep[id] {
			continue
		}
		if err := keyring.Delete(keyringService, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			log.Printf("Failed to remove keyring entry: %v", err)
		}
	}
}

func keyringRefSet(vals []string) map[string]bool {
	refs := make(map[string]bool)
	for _, v := range vals {
		if id, ok := keyringRefID(v); ok {
			refs[id] = true
		}
	}
	return refs
}

func keyringRefID(v string) (string, bool) {
	if strings.HasPrefix(v, keyringPrefix) {
		return strings.TrimPrefix(v, keyringPrefix), true
	}
	return "", false
}

func passwordsOf(conns []ConnConfig) []string {
	out := make([]string, len(conns))
	for i, c := range conns {
		out[i] = c.Password
	}
	return out
}
