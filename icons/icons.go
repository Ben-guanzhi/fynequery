// Package icons embeds a small set of hand-tuned SVG glyphs and exposes them as
// fyne.Resource values so the binary carries no external asset files.
//
// Why custom SVGs instead of theme.XxxIcon():
//   - the default Fyne glyphs read as "heavy" next to the TablePro-flavoured UI;
//   - embedding them compiles the artwork straight into the executable.
//
// Theme adaptation: Fyne's SVG colorizer only tints the `fill` attribute (it
// ignores `stroke`), so every glyph here is built from SOLID FILLED shapes.
// They are wrapped with theme.NewThemedResource so they automatically take the
// active theme's foreground colour (light & dark). Holes (the lens ring, the
// eye pupil) are cut with reversed-wound subpaths inside a single path, which
// Fyne's rasteriser honours; strokes would simply vanish.
// Glyphs are drawn outline-style (2-unit walls on a 24 grid) so they stay
// legible at the 16px the sidebar tree renders them.
package icons

import (
	"embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

//go:embed *.svg
var fs embed.FS

// load reads an embedded SVG file into a static resource.
func load(name string) fyne.Resource {
	data, err := fs.ReadFile(name)
	if err != nil {
		fyne.LogError("icon embed: "+name, err)
		return nil
	}
	return fyne.NewStaticResource(name, data)
}

// themed wraps an embedded SVG so it inherits the active theme's foreground
// colour. The .svg extension on the inner name is what lets Fyne pick the SVG
// decoder, so keep it.
func themed(name string) fyne.Resource {
	return theme.NewThemedResource(load(name))
}

var (
	Table    = themed("table.svg")    // DB table / grid (sidebar leaf)
	View     = themed("view.svg")     // DB view (sidebar leaf)
	Function = themed("function.svg") // stored function / procedure (sidebar leaf)
	Folder   = themed("folder.svg")   // tree branch

	Search   = themed("search.svg")   // find / search
	Cancel   = themed("cancel.svg")   // clear / dismiss (×)
	Edit     = themed("edit.svg")     // edit mode toggle
	Console  = themed("console.svg")  // console / terminal toggle
	Document = themed("document.svg") // generic document / file
	WinMin   = themed("winmin.svg")   // custom chrome: minimise
	WinMax   = themed("winmax.svg")   // custom chrome: maximise / restore
)
