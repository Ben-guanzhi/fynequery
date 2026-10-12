package db

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// Ported from the parent TableProGo project's exportxlsx.go: render the rows
// on screen as a real workbook - typed cells, frozen bold header, fitted
// column widths.

// WorkbookMaxRows bounds one export. The grid only ever holds a page of a
// table, so reaching this means the caller sent something it should not have.
const WorkbookMaxRows = 100000

// workbookSheetName is the one worksheet the export writes.
const workbookSheetName = "查询结果"

// WorkbookColumn is one result column as the grid header reports it: the name
// to write in row 1 and the database type that decides what kind of cell the
// values become.
type WorkbookColumn struct {
	Name string
	Type string
}

// Date and time types whose text form should become a real date cell, so
// Excel can sort, filter and reformat them instead of showing a string. Names
// starting with "timestamp" are handled separately because PostgreSQL spells
// them out with a time zone suffix.
var dateTypes = map[string]bool{
	"date": true, "datetime": true, "datetime2": true, "smalldatetime": true,
}

func isDateColumn(colType string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.Split(colType, "(")[0]))
	if strings.HasPrefix(base, "timestamp") {
		return true
	}
	return dateTypes[base]
}

// The layouts a result can arrive in. runSelect renders a driver's time as
// RFC 3339, so it comes first and covers both the offset and the fraction;
// the rest are dates and times the tables themselves hold as text, and a
// seconds layout also accepts a fraction the layout does not spell out.
var workbookTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
}

func parseWorkbookTime(text string) (time.Time, bool) {
	value := strings.TrimSpace(text)
	if len(value) < 8 {
		return time.Time{}, false
	}
	for _, layout := range workbookTimeLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// workbookStyles are the cell formats the export applies, built once per file
// because every style is a record in the workbook.
type workbookStyles struct {
	header   int
	dateOnly int
	dateTime int
}

// cellKind is what a column's values become in the sheet. The declared type
// says so once for the whole column, so it is worked out while the columns
// are walked rather than re-read from the type string per cell.
type cellKind int

const (
	kindText cellKind = iota
	kindNumber
	kindDate
)

func workbookCellKind(colType string) cellKind {
	switch {
	case isNumericColumn(colType):
		return kindNumber
	case isDateColumn(colType):
		return kindDate
	}
	return kindText
}

// workbookCellValue is what one grid value becomes in the sheet. Numbers and
// dates become real cells; everything else goes in as text, which also keeps a
// value like "=1+1" or "007" from being reinterpreted by Excel. A nil comes
// back as nil so the row writer leaves a genuinely empty cell behind instead of
// spelling out "null".
func workbookCellValue(kind cellKind, value interface{}, styles workbookStyles) interface{} {
	switch v := value.(type) {
	case nil:
		return nil
	case bool:
		return v
	case float64:
		return v
	case int64:
		return v
	case string:
		switch kind {
		case kindNumber:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				return nil
			}
			if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
				return number
			}
			// Text in a numeric column is a display problem, not a reason to
			// refuse the whole file: keep it visible as it is.
			return v
		case kindDate:
			parsed, ok := parseWorkbookTime(v)
			if !ok {
				return v
			}
			style := styles.dateTime
			if parsed.Hour()|parsed.Minute()|parsed.Second() == 0 {
				style = styles.dateOnly
			}
			return excelize.Cell{Value: parsed, StyleID: style}
		}
		return v
	}
	return excelize.Cell{Value: fmt.Sprint(value)}
}

// workbookRow is one grid row in the shape the stream writer wants: a value per
// cell, in column order, so a whole row leaves memory with a single call.
func workbookRow(kinds []cellKind, row []interface{}, styles workbookStyles) []interface{} {
	cells := make([]interface{}, len(row))
	for i, value := range row {
		cells[i] = workbookCellValue(kinds[i], value, styles)
	}
	return cells
}

// displayUnits is how wide a value looks in a column, counted in the ASCII
// characters Excel sizes widths by. Anything from U+1100 up - CJK, Hangul,
// full width punctuation - is drawn about twice as wide as an "i", so it
// counts 2.
func displayUnits(text string) int {
	units := 0
	for _, r := range text {
		if r >= 0x1100 {
			units++
		}
		units++
	}
	return units
}

// cellDisplayUnits is displayUnits for a value the grid holds. A number counts
// as its shortest form; a date arrives as text and so counts as that text,
// which is a few characters more than the cell will show.
func cellDisplayUnits(value interface{}) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return displayUnits(v)
	case bool:
		if v {
			return 5
		}
		return 4
	case float64:
		return len(strconv.FormatFloat(v, 'f', -1, 64))
	case int64:
		return len(strconv.FormatInt(v, 10))
	}
	return displayUnits(fmt.Sprint(value))
}

const (
	workbookMinColWidth = 9.0
	workbookMaxColWidth = 60.0
)

// newWorkbookStyles registers the three formats the export needs.
func newWorkbookStyles(f *excelize.File) (workbookStyles, error) {
	var styles workbookStyles
	header, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return styles, err
	}
	styles.header = header
	// The default date format is the reader's locale, which would give an
	// American "09-22-26" for a Chinese user's export. These say what is
	// stored.
	dateOnlyFormat := "yyyy-mm-dd"
	dateOnly, err := f.NewStyle(&excelize.Style{CustomNumFmt: &dateOnlyFormat})
	if err != nil {
		return styles, err
	}
	styles.dateOnly = dateOnly
	dateTimeFormat := "yyyy-mm-dd hh:mm:ss"
	dateTime, err := f.NewStyle(&excelize.Style{CustomNumFmt: &dateTimeFormat})
	if err != nil {
		return styles, err
	}
	styles.dateTime = dateTime
	return styles, nil
}

// buildWorkbook renders the columns and rows on screen as an xlsx file: a
// bold, frozen header row, one typed cell per value, and a width per column.
func BuildWorkbook(columns []WorkbookColumn, rows [][]interface{}) ([]byte, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("当前结果没有列，无法导出 XLSX")
	}
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	styles, err := newWorkbookStyles(f)
	if err != nil {
		return nil, err
	}
	sheet := f.GetSheetName(0)
	if err := f.SetSheetName(sheet, workbookSheetName); err != nil {
		return nil, err
	}
	sheet = workbookSheetName

	kinds := make([]cellKind, len(columns))
	widths := make([]int, len(columns))
	for i, col := range columns {
		kinds[i] = workbookCellKind(col.Type)
		// Seeded with the header so a short column of long names still fits.
		widths[i] = displayUnits(col.Name)
	}

	// The stream writer refuses a column width once the first row has been
	// written, so the widths are measured over the values first. A short row is
	// refused here too: it would otherwise write a shifted record.
	for r, row := range rows {
		if len(row) != len(columns) {
			return nil, fmt.Errorf("第 %d 行有 %d 个值，和 %d 列对不上", r+1, len(row), len(columns))
		}
		for i, value := range row {
			if w := cellDisplayUnits(value); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Rows stream to disk as they are made instead of being held in the workbook
	// in memory, so a big result does not carry its own size twice.
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return nil, err
	}
	if err := sw.SetPanes(&excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return nil, err
	}
	for i, width := range widths {
		// A cell is drawn with a little padding, and a width is only a guess
		// at how wide the value looks in the reader's font.
		size := min(max(float64(width+2), workbookMinColWidth), workbookMaxColWidth)
		if err := sw.SetColWidth(i+1, i+1, size); err != nil {
			return nil, err
		}
	}

	header := make([]interface{}, len(columns))
	for i, col := range columns {
		header[i] = excelize.Cell{Value: col.Name, StyleID: styles.header}
	}
	if err := sw.SetRow("A1", header); err != nil {
		return nil, err
	}
	for r, row := range rows {
		if err := sw.SetRow("A"+strconv.Itoa(r+2), workbookRow(kinds, row, styles)); err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", r+1, err)
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
