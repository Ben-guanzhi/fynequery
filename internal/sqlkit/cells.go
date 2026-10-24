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
		// Keep fractional seconds (MySQL datetime(6)/timestamp(6)) so an edited
		// cell round-trips without silently truncating precision.
		return plugindriver.CellPayload{
			Kind: "time",
			Time: x.Format("2006-01-02 15:04:05.999999999"),
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
		return cell.Time
	default:
		return ""
	}
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
