package main

// License / About sheet, opened from the welcome screen's License shortcut.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// showLicenseDialog presents the product/version info, license state and the
// local passcode controls.
func showLicenseDialog(w fyne.Window) {
	key := widget.NewEntry()
	key.SetPlaceHolder("XXXX-XXXX-XXXX-XXXX")

	pass := widget.NewPasswordEntry()
	pass.SetPlaceHolder("New passcode")
	status := widget.NewLabel(lockStatusText())

	var dlg dialog.Dialog
	closeBtn := widget.NewButton("Close", func() { dlg.Hide() })
	activateBtn := widget.NewButton("Activate", func() {
		showInfo(w, "License", "This is a local development build.\nLicense activation is not available.")
	})
	activateBtn.Importance = widget.HighImportance

	setPassBtn := widget.NewButton("Set passcode", func() {
		if len(pass.Text) < 4 {
			showInfo(w, "Local passcode", "Choose a passcode of at least 4 characters.")
			return
		}
		if err := setPasscode(pass.Text); err != nil {
			dialog.NewError(err, w).Show()
			return
		}
		pass.SetText("")
		status.SetText(lockStatusText())
		showInfo(w, "Local passcode", "Local passcode set. It will be required at startup.")
	})
	clearPassBtn := widget.NewButton("Remove passcode", func() {
		if err := clearPasscode(); err != nil {
			dialog.NewError(err, w).Show()
			return
		}
		status.SetText(lockStatusText())
	})

	title := widget.NewLabelWithStyle("TablePro", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	body := container.NewVBox(
		title,
		widget.NewLabel("Version 1.0.0 · A native tool for relational databases"),
		widget.NewSeparator(),
		widget.NewLabel("Status: Not licensed (local development build)"),
		widget.NewForm(widget.NewFormItem("License key", key)),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Local passcode", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		status,
		container.NewBorder(nil, nil, nil,
			container.NewHBox(setPassBtn, clearPassBtn), pass),
		container.NewHBox(layout.NewSpacer(), closeBtn, activateBtn),
	)

	dlg = dialog.NewCustom("License", "", body, w)
	dlg.Resize(fyne.NewSize(480, 420))
	dlg.Show()
}

func lockStatusText() string {
	if hasPasscode() {
		return "Lock: enabled"
	}
	return "Lock: disabled"
}
