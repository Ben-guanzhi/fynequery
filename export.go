package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// exportColumns returns base column names (stripping the "(TYPE)" suffix the
// grid shows) for use as CSV/JSON field names.
func exportColumns(gridHeader []string) []string {
	cols := make([]string, len(gridHeader))
	for i, h := range gridHeader {
		cols[i] = stripTypeSuffix(h)
	}
	return cols
}

// stripTypeSuffix removes the trailing " (TYPE)" the grid header appends.
func stripTypeSuffix(h string) string {
	if strings.HasSuffix(h, ")") {
		if idx := strings.LastIndex(h, " ("); idx >= 0 {
			return h[:idx]
		}
	}
	return h
}

// writeCSV renders the grid as RFC 4180 CSV with a header row.
func writeCSV(w io.Writer, gridHeader []string, gridRows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportColumns(gridHeader)); err != nil {
		return err
	}
	for _, row := range gridRows {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// writeJSON renders the grid as a JSON array of objects keyed by column name.
// Duplicate column names are suffixed to stay unique.
func writeJSON(w io.Writer, gridHeader []string, gridRows [][]string) error {
	cols := exportColumns(gridHeader)
	// De-duplicate column names.
	seen := map[string]int{}
	for i, col := range cols {
		seen[col]++
		if seen[col] > 1 {
			cols[i] = fmt.Sprintf("%s_%d", col, seen[col])
		}
	}

	objects := make([]map[string]string, len(gridRows))
	for r, row := range gridRows {
		obj := make(map[string]string, len(cols))
		for i, col := range cols {
			if i < len(row) {
				obj[col] = row[i]
			}
		}
		objects[r] = obj
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(objects)
}