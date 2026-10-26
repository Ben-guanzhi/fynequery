package main

// Query Magics: display-formatting suffixes on a column alias, modeled on
// Beekeeper Studio's "__format__" feature. In a SELECT, name a column like
//
//	SELECT url AS url__format__link,   -- clickable
//	       flag AS flag__format__check,
//	       price AS price__format__money,
//	       ts   AS ts__format__unixtime,
//	       rating AS rating__format__stars,
//	       pct  AS pct__format__progress
//
// and the result grid renders the value pre-formatted. Goto links mirror
// TablePlus' foreign-key jump arrows:
//
//	SELECT user_id AS user_id__goto__users__id
//
// which makes the cell a link that opens the referenced table filtered to
// this value. Magics only change the display, never the data.

import (
	"fmt"
	"image/color"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
)

// magicPrefix is the token that starts a format spec inside a column alias.
const magicPrefix = "__format__"

// gotoPrefix starts a goto spec: "__goto__[schema.]table[__column]".
const gotoPrefix = "__goto__"

// splitMagic decomposes a column alias into (base, spec). spec is either
// "format:<spec>" or "goto:<spec>" (empty when no magic is present).
func splitMagic(alias string) (base, spec string) {
	if i := strings.Index(alias, magicPrefix); i >= 0 {
		return alias[:i], "format:" + alias[i+len(magicPrefix):]
	}
	if i := strings.Index(alias, gotoPrefix); i >= 0 {
		return alias[:i], "goto:" + alias[i+len(gotoPrefix):]
	}
	return alias, ""
}

// magicKinds maps the first word of a format spec to its renderer name.
var magicKinds = map[string]bool{
	"link": true, "email": true, "check": true, "image": true,
	"money": true, "progress": true, "stars": true, "unixtime": true,
	"enum": true,
}

// renderMagic turns (spec, raw value) into (display text, colour, style).
// It reports ok=false when the spec is unknown or the value cannot be shaped
// (the caller then shows the raw value).
func renderMagic(spec, raw string, p *palette) (text string, c color.Color, style fyne.TextStyle, ok bool) {
	spec = strings.TrimPrefix(spec, "format:")
	parts := strings.SplitN(spec, "__", 3) // e.g. money__gbp -> [money, gbp]
	kind := strings.ToLower(parts[0])
	if len(parts) < 1 || !magicKinds[kind] {
		return "", nil, fyne.TextStyle{}, false
	}
	switch kind {
	case "link", "image":
		// Underline + accent colour marks the value as clickable.
		return raw, p.statusLink, fyne.TextStyle{Italic: true}, true
	case "email":
		return raw, p.statusLink, fyne.TextStyle{Italic: true}, true
	case "check":
		if raw == "1" || strings.EqualFold(raw, "true") {
			return "✔", p.cellFloat, fyne.TextStyle{}, true
		}
		if raw == "0" || strings.EqualFold(raw, "false") || raw == "" {
			return "✘", p.rowDeletedBG, fyne.TextStyle{}, true
		}
		return raw, p.cellDefault, fyne.TextStyle{}, false
	case "money":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", nil, fyne.TextStyle{}, false
		}
		cur := "USD"
		if len(parts) > 1 {
			cur = strings.ToUpper(parts[1])
		}
		return fmt.Sprintf("%s %.2f", cur, f), p.cellFloat, fyne.TextStyle{}, true
	case "progress":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", nil, fyne.TextStyle{}, false
		}
		max := 100.0
		if len(parts) > 1 {
			if m, e2 := strconv.ParseFloat(parts[1], 64); e2 == nil {
				max = m
			}
		}
		if max <= 0 {
			return fmt.Sprintf("%g / %g", f, max), p.cellDefault, fyne.TextStyle{}, false
		}
		n := int(f/max*10 + 0.5)
		if n > 10 {
			n = 10
		}
		return strings.Repeat("█", n) + strings.Repeat("░", 10-n) + fmt.Sprintf(" %g/%g", f, max),
			p.cellInt, fyne.TextStyle{}, true
	case "stars":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", nil, fyne.TextStyle{}, false
		}
		max := 5.0
		if len(parts) > 1 {
			if m, e2 := strconv.ParseFloat(parts[1], 64); e2 == nil {
				max = m
			}
		}
		filled := int(f + 0.5)
		if filled > int(max) {
			filled = int(max)
		}
		blank := int(max) - filled
		return strings.Repeat("★", filled) + strings.Repeat("☆", blank) + fmt.Sprintf(" (%g)", f),
			p.pkHeader, fyne.TextStyle{}, true
	case "unixtime":
		return formatUnixTime(raw, parts[1:]), p.cellDate, fyne.TextStyle{}, true
	}
	return raw, p.cellDefault, fyne.TextStyle{}, false
}

// formatUnixTime renders a unix timestamp with an optional scale arg
// (s / ms / us / ns; default auto-detect).
func formatUnixTime(raw string, args []string) string {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return raw
	}
	scale := "s"
	if len(args) > 0 {
		scale = strings.ToLower(args[0])
	}
	switch scale {
	case "ms":
		v = v * int64(time.Millisecond)
	case "us":
		v = v * int64(time.Microsecond)
	case "ns":
		v = v * int64(time.Nanosecond)
	default: // auto: very large values are sub-second
		switch {
		case v > 1e16:
			v *= int64(time.Nanosecond)
		case v > 1e13:
			v *= int64(time.Microsecond)
		case v > 1e10:
			v *= int64(time.Millisecond)
		default:
			v *= int64(time.Second)
		}
	}
	return time.Unix(0, v).Local().Format("2006-01-02 15:04:05")
}

// gotoTarget parses the spec following "__goto__" into (table, column).
// The column separator is "__" (splitMagic already cut the leading
// "__goto__" off), with an optional "[col]" form accepted too:
//
//	"users__id"  -> ("users", "id")
//	"users"      -> ("users", "")
//	"users[id]"  -> ("users", "id")
func gotoTarget(spec string) (table, column string) {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, '['); i >= 0 {
		if end := strings.IndexByte(spec, ']'); end > i {
			column = spec[i+1 : end]
			spec = spec[:i]
		} else {
			spec = spec[:i]
		}
	}
	if i := strings.LastIndex(spec, "__"); i >= 0 {
		table, column = spec[:i], spec[i+2:]
		return strings.TrimSpace(table), strings.TrimSpace(column)
	}
	return spec, column
}

// magicTap executes the click action for a link/email/goto cell. It reports
// whether the tap was consumed. base is the magic-stripped column alias.
func magicTap(spec, base, raw string, t *dataTable) bool {
	if strings.HasPrefix(spec, "format:") {
		rest := spec[len("format:"):]
		kind := strings.ToLower(strings.SplitN(rest, "__", 2)[0])
		switch kind {
		case "link":
			openURL(raw)
			return true
		case "email":
			openURL("mailto:" + raw)
			return true
		case "image":
			openURL(raw)
			return true
		}
		return false
	}
	if strings.HasPrefix(spec, "goto:") {
		if t.onGoto == nil || raw == "" {
			return false
		}
		table, col := gotoTarget(spec[len("goto:"):])
		if table == "" {
			return false
		}
		// When the magic omits the filter column, use the cell's own alias
		// base (e.g. "user_id__goto__users" filters users.user_id).
		if col == "" {
			col = base
		}
		t.onGoto(table, col, raw)
		return true
	}
	return false
}

// openURL opens a URL / mailto link in the platform default browser or mail
// client.
func openURL(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		appLogf("openURL %s: %v", u, err)
	}
}
