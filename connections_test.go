package main

import (
	"runtime"
	"strings"
	"testing"

	"tablepro-fyne/internal/db"
)

// TestSecretStorageWarning pins the dialog's honesty about saved passwords: the
// platform that seals secrets with a per-user key says nothing, the one that
// cannot says so and names the risk instead of implying encryption.
func TestSecretStorageWarning(t *testing.T) {
	// The data layer's answer must match the build tag it came from.
	if got := db.SecretsSealed(); got != (runtime.GOOS == "windows") {
		t.Fatalf("SecretsSealed() = %v on %s, want it to track the platform", got, runtime.GOOS)
	}
	if note := secretStorageNote(true); note != "" {
		t.Errorf("sealed platform should stay quiet, got %q", note)
	}
	note := secretStorageNote(false)
	if !strings.Contains(note, "明文") {
		t.Errorf("unsealed warning must name the clear-text storage, got %q", note)
	}
	if !strings.Contains(note, "0600") {
		t.Errorf("unsealed warning must say what does protect the file, got %q", note)
	}
}

// TestSSLModesByEngine pins the 加密 dropdown's label-to-mode mapping: SQL
// Server gets the modes its driver actually parses (with verify-and-encrypt as
// the default entry), MySQL only the one value its connector acts on, and a
// stored mode the current engine does not name survives the switch instead of
// being silently dropped.
func TestSSLModesByEngine(t *testing.T) {
	mssql := sslChoicesFor(db.DbTypeSQLServer)
	for label, want := range map[string]string{
		"加密并校验证书（默认）":    "true",
		"加密，信任服务器证书（自签）": "trust",
		"不加密": "disable",
	} {
		if got := sslValueFor(mssql, label); got != want {
			t.Errorf("mssql label %q = %q, want %q", label, got, want)
		}
	}
	// The default entry must be the first one the dialog offers.
	if sslOptionLabels(mssql, "")[0] != "加密并校验证书（默认）" {
		t.Errorf("mssql options = %v, want verify-and-encrypt first", sslOptionLabels(mssql, ""))
	}

	// "strict" is not offered: this driver cannot parse it, and a mode that
	// silently degrades is the bug this list exists to prevent.
	for _, ch := range sslChoicesFor(db.DbTypeSQLServer) {
		if ch.value == "strict" {
			t.Error("SQL Server must not offer a mode its driver rejects")
		}
	}

	if got := sslValueFor(sslChoicesFor(db.DbTypeMySQL), "加密"); got != "true" {
		t.Errorf("mysql 加密 = %q, want true (the only value the connector acts on)", got)
	}

	// A mode from another engine's vocabulary is carried through as its own
	// label and maps back to itself.
	carry := "verify-full"
	found := false
	for _, label := range sslOptionLabels(mssql, carry) {
		if label == carry {
			found = true
		}
	}
	if !found {
		t.Errorf("labels for mssql with carried mode = %v, want %q kept", sslOptionLabels(mssql, carry), carry)
	}
	if got := sslValueFor(mssql, sslChoiceLabel(mssql, carry)); got != carry {
		t.Errorf("round trip = %q, want %q", got, carry)
	}

	// SQLite and Oracle have no TLS list of their own; they keep the list the
	// dialog already stored modes come from, rather than losing them.
	if len(sslChoicesFor(db.DbTypeSQLite)) != len(sslChoicesFor(db.DbTypePostgres)) {
		t.Error("engines without a TLS vocabulary must fall back, not go blank")
	}
}
