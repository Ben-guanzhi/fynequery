package sqlkit

import (
	"fmt"
	"strconv"
	"time"

	"github.com/dbcore/dbcore/plugindriver"
)

// The cell is the kernel's tagged form: a value keeps its kind on the way to the
// plugin and back.
//
// The old shape sent every cell as a string, so a bigint key came back as
// "9223372036854775807" and then as a float64 — already one off. A grid cursor
// built on that points at a neighbouring row. The tagged form is what the kernel
// uses, and using it here means the two clients' plugins speak one cell format.

// FormatCell turns a scanned value into its tagged cell.
func FormatCell(v interface{}) plugindriver.CellPayload {
	switch x := v.(type) {
	case nil:
		return plugindriver.CellPayload{Kind: "null"}
	case []byte:
		return plugindriver.CellPayload{Kind: "blob", Blob: x}
	case string:
		return plugindriver.CellPayload{Kind: "text", Text: x}
	case time.Time:
		// The wire format is the protocol's RFC3339-with-nanos (see
		// plugindriver.DecodeValue's timeFormat), NOT Go's default layout: the
		// host parses with that exact layout, so a space-separated local
		// spelling fails to decode and the whole result errors out. UTC keeps
		// the instant unambiguous across the process boundary; fractional
		// seconds survive so MySQL datetime(6)/timestamp(6) round-trip.
		return plugindriver.CellPayload{
			Kind: "time",
			Time: x.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
	case int:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case int8:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case int16:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case int32:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case int64:
		return plugindriver.CellPayload{Kind: "int", Int: x}
	case uint:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case uint8:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case uint16:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case uint32:
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case uint64:
		// A uint64 above the signed range does not fit an int64; the text form
		// keeps it, which is what a big unsigned key needs.
		if x > uint64(1)<<63-1 {
			return plugindriver.CellPayload{Kind: "text", Text: strconv.FormatUint(x, 10)}
		}
		return plugindriver.CellPayload{Kind: "int", Int: int64(x)}
	case float64:
		return plugindriver.CellPayload{Kind: "float", Flt: x}
	case float32:
		return plugindriver.CellPayload{Kind: "float", Flt: float64(x)}
	case bool:
		return plugindriver.CellPayload{Kind: "bool", Bool: x}
	default:
		return plugindriver.CellPayload{Kind: "text", Text: fmt.Sprintf("%v", x)}
	}
}

// cellText is the text a cell shows. A NULL has no text at all — it is not the
// empty string, and the grid says so.
//
// The time cell is the one kind whose wire and display spellings differ: the
// wire is RFC3339-with-nanos (that is what the host decodes with), while a grid
// should show something a human reads. Parse and re-render here — the display
// conversion lives at the display edge, so the protocol keeps one format.
// A value that does not parse falls through as its wire text rather than
// vanishing: showing the raw instant beats showing an empty cell.
func cellText(cell plugindriver.CellPayload) string {
	switch cell.Kind {
	case "int":
		return strconv.FormatInt(cell.Int, 10)
	case "float":
		return strconv.FormatFloat(cell.Flt, 'g', -1, 64)
	case "text":
		return cell.Text
	case "bool":
		return strconv.FormatBool(cell.Bool)
	case "blob":
		return string(cell.Blob)
	case "time":
		if t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", cell.Time); err == nil {
			return formatDisplayTime(t)
		}
		return cell.Time
	default:
		return ""
	}
}

// formatDisplayTime renders an instant for the grid: local time, seconds
// precision, with fractional digits only when the value actually carries them
// (a datetime(6) keeps its microseconds; a plain timestamp stays tidy).
func formatDisplayTime(t time.Time) string {
	local := t.Local()
	if local.Nanosecond() == 0 {
		return local.Format("2006-01-02 15:04:05")
	}
	return local.Format("2006-01-02 15:04:05.999999999")
}

// CellToString renders a cell as the nullable string the UI works with. The
// conversion happens at the edge of the UI, so the wire keeps its kinds.
func CellToString(cell plugindriver.CellPayload) *string {
	if cell.Kind == "null" || cell.Kind == "" {
		return nil
	}
	text := cellText(cell)
	return &text
}

// StringToCell is the inverse: a UI cell becomes a tagged cell. An empty string
// is an empty string, not a NULL — only a nil pointer is a NULL.
func StringToCell(value *string) plugindriver.CellPayload {
	if value == nil {
		return plugindriver.CellPayload{Kind: "null"}
	}
	return plugindriver.CellPayload{Kind: "text", Text: *value}
}
