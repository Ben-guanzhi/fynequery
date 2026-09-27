//go:build windows

package main

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

// unprotectSecret opens a sealed password; plain-text legacy values pass through.
func unprotectSecret(stored string) (string, error) {
	if !strings.HasPrefix(stored, secretPrefix) {
		return stored, nil
	}
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