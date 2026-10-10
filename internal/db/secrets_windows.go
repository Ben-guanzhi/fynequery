//go:build windows

package db

import (
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const cryptProtectUIForbidden = 0x1

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procLocalFree          = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

type cryptDataBlob struct {
	size uint32
	data *byte
}

func callCrypt32(proc *syscall.LazyProc, in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, nil
	}
	inBlob := cryptDataBlob{size: uint32(len(in)), data: &in[0]}
	var outBlob cryptDataBlob
	ret, _, err := proc.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0, 0, 0, 0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&outBlob)),
	)
	runtime.KeepAlive(&inBlob)
	if ret == 0 {
		return nil, err
	}
	out := make([]byte, outBlob.size)
	copy(out, unsafe.Slice(outBlob.data, outBlob.size))
	procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.data)))
	return out, nil
}

// SecretsSealed reports that this platform stores a saved password in a form
// only the current local user can undo, so the connection dialog has nothing
// to warn about. The !windows build answers from a keyring probe instead.
func SecretsSealed() bool { return true }

// protectSecret seals a password with DPAPI, scoped to the current Windows user.
func protectSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	ciphertext, err := callCrypt32(procCryptProtectData, []byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("encrypt password: %w", err)
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// unsealDPAPI opens a "dpapi:" value; unprotectSecret only calls it when the
// prefix is present. A value sealed by another Windows account or machine
// fails here — the caller clears the password and asks the user to retype it.
func unsealDPAPI(stored string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("decode password: %w", err)
	}
	plaintext, err := callCrypt32(procCryptUnprotectData, ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt password: %w", err)
	}
	return string(plaintext), nil
}
