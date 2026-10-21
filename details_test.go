package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestRowSelectionCallbacks(t *testing.T) {
	test.NewApp()
	s := func(v string) *string { return &v }
	data := &TableData{
		Cols: []ColumnInfo{
			{Name: "id", Type: "int", Key: "PK"},
			{Name: "name", Type: "varchar"},
		},
		Rows:    [][]*string{{s("1"), s("a")}},
		Fetched: 1, Total: 1,
	}
	g := newDataTable(data, newStatusBar())
	var sel []int
	g.onRowSelect = func(r int) { sel = append(sel, r) }
	g.selectRow(0)
	if len(sel) != 1 || sel[0] != 0 {
		t.Fatalf("onRowSelect = %v, want [0]", sel)
	}
	if g.selRow != 0 {
		t.Fatalf("selRow = %d, want 0", g.selRow)
	}
	// Pending edits are reflected in rowValues.
	g.setCell(0, 0, s("99"))
	if got := *g.rowValues(0)[0]; got != "99" {
		t.Fatalf("rowValues[0] = %q, want 99", got)
	}
	if g.rowValues(5) != nil {
		t.Fatal("rowValues out of range should be nil")
	}
}

func TestDetailsPaneRow(t *testing.T) {
	test.NewApp()
	d := newDetailsPane()
	w := test.NewWindow(d)
	defer w.Close()

	if d.slot == nil {
		t.Fatal("renderer did not initialize slot")
	}
	// No row selected yet: prompt, no fields.
	if len(d.fields) != 0 {
		t.Fatalf("initial fields = %d, want 0", len(d.fields))
	}

	v := "a"
	var (
		gotValSet bool
		gotVal    *string
	)
	d.onSet = func(col int, val *string) { gotVal, gotValSet = val, true }
	d.setRow([]detailsField{
		{Name: "id", Type: "int", Value: &v, Col: 0},
		{Name: "created", Type: "datetime", Value: nil, Col: 1},
	})
	if len(d.fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(d.fields))
	}

	// Clicking the value text opens the editor; the chevron opens the menu.
	edited, menu := false, false
	vb := newValueButton(&v)
	vb.Resize(fyne.NewSize(200, 30))
	vb.onEdit = func() { edited = true }
	vb.onMenu = func(fyne.Position) { menu = true }
	test.TapAt(vb, fyne.NewPos(20, 15))
	if !edited {
		t.Fatal("tapping the value did not open the editor")
	}
	test.TapAt(vb, fyne.NewPos(195, 15))
	if !menu {
		t.Fatal("tapping the chevron did not open the menu")
	}

	d.applyValue(1, nil)
	if !gotValSet || gotVal != nil {
		t.Fatalf("applyValue: set=%v val=%v", gotValSet, gotVal)
	}
}

// TestDetailsValueBoxFlattensMultiLine pins that a multi-line value (a
// pretty-printed JSON blob, a text field with line breaks) is flattened onto
// one line in the details value box, matching the grid cell; otherwise the
// box would grow to fit every line and shove the fields below it around.
func TestDetailsValueBoxFlattensMultiLine(t *testing.T) {
	test.NewApp()
	raw := "{\n  \"name\": \"A  B\",\n  \"age\": 30\n}"
	vb := newValueButton(&raw)
	w := test.NewWindow(vb)
	w.Resize(fyne.NewSize(220, 60))
	if vb.label == nil {
		t.Fatal("valueButton renderer did not build its label")
	}
	if got := vb.label.Text; strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("valueButton did not flatten multi-line value: %q", got)
	}
	if got := vb.label.Text; strings.Contains(got, "\n") {
		t.Fatalf("valueButton kept a newline: %q", got)
	}
	w.Close()
}

func TestIsDateTimeType(t *testing.T) {
	cases := map[string]bool{
		"datetime": true, "timestamp": true, "date": true, "time": true,
		"int": false, "varchar": false, "decimal": false,
	}
	for typ, want := range cases {
		if got := isDateTimeType(typ); got != want {
			t.Fatalf("isDateTimeType(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestDetailsPaneTable(t *testing.T) {
	test.NewApp()
	d := newDetailsPane()
	w := test.NewWindow(d)
	defer w.Close()

	d.setTable("users", []ColumnInfo{
		{Name: "id", Type: "int", Key: "PK"},
		{Name: "email", Type: "varchar", Nullable: true},
	})
	if len(d.fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(d.fields))
	}
	for _, f := range d.fields {
		if !f.ReadOnly {
			t.Fatalf("table structure field %q should be read-only", f.Name)
		}
	}
	if desc := *d.fields[0].Value; !strings.Contains(desc, "PK") || !strings.Contains(desc, "NOT NULL") {
		t.Fatalf("id description = %q", desc)
	}
	if desc := *d.fields[1].Value; strings.Contains(desc, "NOT NULL") {
		t.Fatalf("email should be nullable: %q", desc)
	}
}
