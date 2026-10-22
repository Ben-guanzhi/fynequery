package main

// Local passcode lock (TablePlus's "local passcode"): a salted SHA-256 hash
// stored next to the saved connections. When set, the app asks for it at
// startup before showing the welcome screen.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	hexenc "encoding/hex"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

type lockFile struct {
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

func lockPath() string { return filepath.Join(configDir(), "lock.json") }

// hasPasscode reports whether a local passcode has been configured.
func hasPasscode() bool {
	_, err := os.Stat(lockPath())
	return err == nil
}

func hashPasscode(pw, salt string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + pw))
	return hexenc.EncodeToString(sum[:])
}

// setPasscode stores a salted hash of pw (replacing any existing passcode).
func setPasscode(pw string) error {
	if pw == "" {
		return fmt.Errorf("passcode must not be empty")
	}
	saltb := make([]byte, 16)
	if _, err := rand.Read(saltb); err != nil {
		return err
	}
	salt := hexenc.EncodeToString(saltb)
	raw, err := json.Marshal(lockFile{Salt: salt, Hash: hashPasscode(pw, salt)})
	if err != nil {
		return err
	}
	return os.WriteFile(lockPath(), raw, 0o600)
}

// clearPasscode removes the local passcode.
func clearPasscode() error {
	err := os.Remove(lockPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// verifyPasscode reports whether pw matches the stored passcode.
func verifyPasscode(pw string) bool {
	raw, err := os.ReadFile(lockPath())
	if err != nil {
		return false
	}
	var lf lockFile
	if json.Unmarshal(raw, &lf) != nil {
		return false
	}
	got := hashPasscode(pw, lf.Salt)
	return subtle.ConstantTimeCompare([]byte(got), []byte(lf.Hash)) == 1
}

// showLockWindow blocks startup behind the passcode prompt; onUnlock runs
// once the correct passcode is entered.
func showLockWindow(a fyne.App, onUnlock func()) {
	w := a.NewWindow("TablePro")
	w.Resize(fyne.NewSize(420, 280))

	title := canvas.NewText("TablePro is locked", pal().foreground)
	title.TextSize = 20
	title.TextStyle.Bold = true
	sub := canvas.NewText("Enter your local passcode to continue.", pal().welcomeFG)
	sub.TextSize = 12

	entry := widget.NewPasswordEntry()
	entry.SetPlaceHolder("Passcode")
	errText := canvas.NewText("", color.NRGBA{R: 0xE5, G: 0x39, B: 0x35, A: 0xFF}) // error red, not the accent blue
	errText.TextSize = 12

	unlock := func() {
		if verifyPasscode(entry.Text) {
			w.Close()
			if onUnlock != nil {
				onUnlock()
			}
			return
		}
		errText.Text = "Incorrect passcode"
		errText.Refresh()
		entry.SetText("")
	}
	entry.OnSubmitted = func(string) { unlock() }

	btn := widget.NewButton("Unlock", unlock)
	btn.Importance = widget.HighImportance

	body := container.NewVBox(title, sub, widget.NewSeparator(), entry, btn, errText)
	w.SetContent(newPanelBackground(container.NewPadded(body),
		func() color.Color { return pal().bg }))
	w.Canvas().Focus(entry)
	w.Show()
}
