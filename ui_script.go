package main

import (
	"fmt"
	"io"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
)

// Script files are the portable half of editor persistence: a tab draft
// survives a restart of this app, a .sql file survives machines and people.
// The transfer helpers take streams, so the dialogs stay thin wrappers and the
// logic is testable without a file chooser.

// maxScriptBytes caps a hand-picked file. Anything larger is almost certainly
// not SQL, and dropping it into the editor would stall the layout pass.
const maxScriptBytes = 8 << 20

// loadScript reads a script into the active tab's editor.
func (c *Client) loadScript(r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, maxScriptBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxScriptBytes {
		return fmt.Errorf("文件超过 %d MB，未载入", maxScriptBytes/(1<<20))
	}
	text := string(data)
	// A NUL byte means a binary file was picked by mistake; the editor would
	// accept it and produce nonsense.
	if strings.ContainsRune(text, 0) {
		return fmt.Errorf("看起来不是文本文件")
	}
	c.tabs.entry.setTextSilently(text)
	c.markTabDirty(c.tabs.entry)
	c.focusEditor()
	return nil
}

// saveScript writes the active tab's text out, LF-normalised so a script
// shared with a non-Windows tool reads cleanly there.
func (c *Client) saveScript(w io.Writer) error {
	_, err := io.WriteString(w, strings.ReplaceAll(c.tabs.entry.Text, "\r\n", "\n"))
	return err
}

func (c *Client) showOpenScriptDialog() {
	dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			c.setStatus("打开失败：" + err.Error())
			return
		}
		if r == nil {
			return // cancelled
		}
		defer r.Close()
		name := r.URI().Name()
		if err := c.loadScript(r); err != nil {
			c.setStatus("打开失败：" + err.Error())
			return
		}
		c.setStatus("已载入脚本 " + name)
	}, c.win).Show()
}

func (c *Client) showSaveScriptDialog() {
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			c.setStatus("保存失败：" + err.Error())
			return
		}
		if w == nil {
			return // cancelled
		}
		name := w.URI().Name()
		werr := c.saveScript(w)
		if cerr := w.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			c.setStatus("保存失败：" + werr.Error())
			return
		}
		c.setStatus("已保存脚本 " + name)
	}, c.win)
	d.SetFileName("query.sql")
	d.Show()
}
