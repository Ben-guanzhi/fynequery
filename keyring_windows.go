//go:build windows

package main

// Windows Credential Manager storage for connection passwords, via the
// wincred C API (ADVAPI32) through golang.org/x/sys/windows — no extra
// dependency. Credentials are stored per-connection under
// "TablePro\fynequery\<name>" as a generic credential.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credGeneric = 1 // CRED_TYPE_GENERIC
	credPersist = 2 // CRED_PERSIST_LOCAL_MACHINE
)

// credWin mirrors the CREDENTIALW structure (advapi32) in declaration order;
// Go's natural field alignment reproduces the C layout (size 80 on x64).
type credWin struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        int64 // FILETIME
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32      = windows.NewLazyDLL("advapi32.dll")
	procCredWrite = advapi32.NewProc("CredWriteW")
	procCredRead  = advapi32.NewProc("CredReadW")
	procCredFree  = advapi32.NewProc("CredFree")
	procCredDel   = advapi32.NewProc("CredDeleteW")
)

func credTarget(name string) string { return "TablePro\\fynequery\\" + name }

// keyringAvailable reports whether wincred works on this platform.
func keyringAvailable() bool { return true }

// keyringSet stores value under the connection's credential target,
// replacing any existing credential.
func keyringSet(name, value string) error {
	target, err := windows.UTF16PtrFromString(credTarget(name))
	if err != nil {
		return err
	}
	if value == "" {
		// empty value = clear the stored credential
		r, _, e2 := procCredDel.Call(uintptr(unsafe.Pointer(target)), uintptr(credGeneric), 0)
		if r != 0 || e2 == nil || errors.Is(e2, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return e2
	}
	blob := []byte(value)
	c := credWin{
		Type:               credGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersist,
	}
	r, _, err := procCredWrite.Call(uintptr(unsafe.Pointer(&c)), 0)
	if r == 0 {
		return err
	}
	return nil
}

// keyringGet reads the credential for a connection; found is false when the
// credential does not exist.
func keyringGet(name string) (string, bool) {
	target, err := windows.UTF16PtrFromString(credTarget(name))
	if err != nil {
		return "", false
	}
	var c *credWin
	r, _, _ := procCredRead.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credGeneric),
		0,
		uintptr(unsafe.Pointer(&c)),
	)
	if r == 0 || c == nil || c.CredentialBlob == nil || c.CredentialBlobSize == 0 {
		if c != nil {
			procCredFree.Call(uintptr(unsafe.Pointer(c)))
		}
		return "", false // absent or unreadable
	}
	v := string(unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize))
	procCredFree.Call(uintptr(unsafe.Pointer(c)))
	return v, true
}

// keyringDelete removes the credential (absent credentials are not an error).
func keyringDelete(name string) error {
	target, err := windows.UTF16PtrFromString(credTarget(name))
	if err != nil {
		return err
	}
	r, _, errno := procCredDel.Call(uintptr(unsafe.Pointer(target)), uintptr(credGeneric), 0)
	if r != 0 || errno == nil || errors.Is(errno, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return errno
}
