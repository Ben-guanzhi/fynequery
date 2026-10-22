package icons

import (
	"encoding/xml"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// TestIconEmbed verifies every embedded glyph loads and is well-formed XML so a
// typo in an SVG can never ship silently inside the binary.
func TestIconEmbed(t *testing.T) {
	test.NewApp() // so theme.Color() resolves inside ThemedResource.Content()
	cases := map[string]fyne.Resource{
		"table":    Table,
		"view":     View,
		"function": Function,
		"folder":   Folder,
		"search":   Search,
		"cancel":   Cancel,
		"edit":     Edit,
		"console":  Console,
		"document": Document,
	}
	for name, res := range cases {
		if res == nil {
			t.Fatalf("%s: resource is nil (embed missing?)", name)
		}
		data := res.Content()
		if len(data) == 0 {
			t.Fatalf("%s: empty content", name)
		}
		if err := xml.Unmarshal(data, &struct{}{}); err != nil {
			t.Errorf("%s: SVG is not well-formed XML: %v", name, err)
		}
	}
}
