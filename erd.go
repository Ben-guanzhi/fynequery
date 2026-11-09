package main

// ER diagram (TablePlus "Diagram" read-only slice): a scrollable canvas of
// table boxes joined by straight foreign-key lines. Tables are draggable
// (fyne.Draggable) into place; the diagram opens in its own window.

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// ---- model -----------------------------------------------------------------

type erCol struct {
	Name string
	PK   bool
}

type erTable struct {
	Name string
	Cols []erCol
	Pos  fyne.Position // current box position (draggable)
}

type erFK struct {
	From, FromCol string
	To, ToCol     string
}

type erModel struct {
	Tables []erTable
	FKs    []erFK
}

const (
	erdBoxW   float32 = 180
	erdTitleH float32 = 24
	erdRowH   float32 = 16
	erdGap    float32 = 48
	erdMaxTb          = 60 // cap tables per diagram to keep the fetch bounded

	// erLayoutSweeps is the fixed number of barycenter passes. Crossing
	// minimization is NP-hard even to verify, so the heuristic is the point:
	// a fixed bound keeps the layout deterministic (identical for any input
	// order) and O(sweeps · edges) — convergence probing would trade that
	// for nothing measurable at diagram scale.
	erLayoutSweeps = 4
)

// erBoxSize returns the box dimensions for a table (title + one row per column).
func erBoxSize(t erTable) fyne.Size {
	rows := len(t.Cols)
	if rows < 2 {
		rows = 2
	}
	return fyne.NewSize(erdBoxW, erdTitleH+float32(rows)*erdRowH)
}

// fetchERModel builds the diagram model: tables + columns, plus FKs.
func (ws *workspace) fetchERModel(sess db.Session, driver string) (*erModel, error) {
	objs, err := sess.Tables()
	if err != nil {
		return nil, err
	}
	m := &erModel{}
	for _, o := range objs {
		if o.Kind != "table" || len(m.Tables) >= erdMaxTb {
			continue
		}
		cols, err := sess.Columns(o.Name)
		if err != nil {
			continue
		}
		t := erTable{Name: o.Name}
		for _, c := range cols {
			t.Cols = append(t.Cols, erCol{Name: c.Name, PK: c.Key == "PK"})
		}
		m.Tables = append(m.Tables, t)
	}
	sort.SliceStable(m.Tables, func(i, j int) bool {
		return m.Tables[i].Name < m.Tables[j].Name
	})
	names := make([]string, len(m.Tables))
	for i, t := range m.Tables {
		names[i] = t.Name
	}
	m.FKs = fetchERForeignKeys(sess, driver, names)
	layoutERModel(m)
	return m, nil
}

// layoutERModel assigns initial positions with the same left-to-right
// layered + barycenter layout as TableProGo's erLayout.ts (ported, not
// re-derived): a table's column is one past the deepest table it
// references, so referenced tables sit left of their references; a
// recursion guard makes circular FKs resolve instead of overflowing.
// Within a column the order starts alphabetical and goes through a few
// barycenter sweeps: each column reorders by the average row of its
// neighbours in the adjacent column, which pulls connected tables level
// with each other. Minimizing crossings is NP-hard even to check, so the
// fixed sweep count is the point — determinism over optimality.
func layoutERModel(m *erModel) {
	if len(m.Tables) == 0 {
		return
	}
	// Deterministic FK order first: the sweeps sum neighbour positions, so
	// the sum must not depend on catalog row order. Full-key sort — the same
	// discipline erLayout.ts applies to its edges.
	sort.SliceStable(m.FKs, func(i, j int) bool {
		a, b := m.FKs[i], m.FKs[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.FromCol != b.FromCol {
			return a.FromCol < b.FromCol
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.ToCol < b.ToCol
	})

	// name → table index for O(1) lookups.
	idx := make(map[string]int, len(m.Tables))
	for i := range m.Tables {
		idx[m.Tables[i].Name] = i
	}

	// Layering: DAG longest path with a cycle guard (depth > 1000 or
	// re-entering a node mid-computation resolves to level 0).
	level := make(map[string]int, len(m.Tables))
	visiting := make(map[string]bool, len(m.Tables))
	var levelOf func(name string, depth int) int
	levelOf = func(name string, depth int) int {
		if depth > 1000 {
			return 0
		}
		if l, ok := level[name]; ok {
			return l
		}
		if visiting[name] {
			return 0 // cycle: break it
		}
		visiting[name] = true
		l := 0
		for _, fk := range m.FKs {
			if fk.From != name || fk.To == name {
				continue
			}
			if _, ok := idx[fk.To]; !ok {
				continue // cross-schema ref not in this diagram
			}
			if l2 := levelOf(fk.To, depth+1) + 1; l2 > l {
				l = l2
			}
		}
		visiting[name] = false
		level[name] = l
		return l
	}
	for _, t := range m.Tables {
		levelOf(t.Name, 0)
	}

	maxLevel := 0
	for _, t := range m.Tables {
		if l := level[t.Name]; l > maxLevel {
			maxLevel = l
		}
	}
	columns := make([][]int, maxLevel+1) // per-column table indices, layout order
	for i := range m.Tables {
		columns[level[m.Tables[i].Name]] = append(columns[level[m.Tables[i].Name]], i)
	}
	// Alphabetical seed per column. Codepoint order, not localeCompare:
	// byte-identical on every machine (the upstream TS version's ICU
	// dependency is exactly the determinism hazard this port drops).
	for _, col := range columns {
		sort.SliceStable(col, func(a, b int) bool {
			return m.Tables[col[a]].Name < m.Tables[col[b]].Name
		})
	}

	// Undirected adjacency over the drawn edges (self-references add
	// nothing to a layering).
	neighbors := make(map[string][]string, len(m.Tables))
	link := func(a, b string) {
		neighbors[a] = append(neighbors[a], b)
		neighbors[b] = append(neighbors[b], a)
	}
	for _, fk := range m.FKs {
		if fk.From == fk.To {
			continue
		}
		if _, ok := idx[fk.From]; !ok {
			continue
		}
		if _, ok := idx[fk.To]; !ok {
			continue
		}
		link(fk.From, fk.To)
	}

	// Barycenter sweeps: each pass walks the columns in one direction and
	// reorders every column by its neighbours' average row in the adjacent
	// one; alternating direction lets pulls from both sides settle.
	order := make([][]string, len(columns))
	for ci, col := range columns {
		for _, ti := range col {
			order[ci] = append(order[ci], m.Tables[ti].Name)
		}
	}
	for sweep := 0; sweep < erLayoutSweeps; sweep++ {
		forward := sweep%2 == 0
		cols := make([]int, len(order))
		for i := range cols {
			cols[i] = i
		}
		if !forward {
			for i, j := 0, len(cols)-1; i < j; i, j = i+1, j-1 {
				cols[i], cols[j] = cols[j], cols[i]
			}
		}
		for _, c := range cols {
			adj := c - 1
			if !forward {
				adj = c + 1
			}
			if adj < 0 || adj >= len(order) || len(order[c]) < 2 {
				continue
			}
			// Current rows of every table. Rebuilt per column: the sweeps
			// mutate order in place, so later columns must see updates.
			pos := make(map[string]int, len(m.Tables))
			for _, col := range order {
				for i, name := range col {
					pos[name] = i
				}
			}
			type entry struct {
				name string
				key  float64
			}
			entries := make([]entry, len(order[c]))
			for i, name := range order[c] {
				sum, count := 0, 0
				for _, nb := range neighbors[name] {
					if level[nb] == adj {
						sum += pos[nb]
						count++
					}
				}
				// A table without neighbours in the adjacent column keeps
				// its current slot as the sort key, so isolated tables do
				// not drift.
				key := float64(i)
				if count > 0 {
					key = float64(sum) / float64(count)
				}
				entries[i] = entry{name: name, key: key}
			}
			sort.SliceStable(entries, func(a, b int) bool {
				if entries[a].key != entries[b].key {
					return entries[a].key < entries[b].key
				}
				return entries[a].name < entries[b].name
			})
			for i := range entries {
				order[c][i] = entries[i].name
			}
		}
	}

	// Position assignment: column-major, y accumulating per column.
	for ci, col := range order {
		y := erdGap
		for _, name := range col {
			t := &m.Tables[idx[name]]
			sz := erBoxSize(*t)
			t.Pos = fyne.NewPos(erdGap+float32(ci)*(erdBoxW+erdGap), y)
			y += sz.Height + erdGap
		}
	}
}

// erFKQuery returns the information_schema FK query for a driver, or "" when
// the driver has no single-query diagram support (SQLite discovers FKs
// per-table via PRAGMA, handled in fetchERForeignKeys).
func erFKQuery(driver string) string {
	switch driver {
	case "mysql", "mariadb":
		return "SELECT TABLE_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME " +
			"FROM information_schema.KEY_COLUMN_USAGE " +
			"WHERE TABLE_SCHEMA = DATABASE() AND REFERENCED_TABLE_NAME IS NOT NULL"
	case "postgres", "postgresql":
		return "SELECT kcu.TABLE_NAME, kcu.COLUMN_NAME, kcu.REFERENCED_TABLE_NAME, kcu.REFERENCED_COLUMN_NAME " +
			"FROM information_schema.KEY_COLUMN_USAGE kcu " +
			"WHERE kcu.TABLE_SCHEMA = current_schema() AND kcu.REFERENCED_TABLE_NAME IS NOT NULL"
	case "mssql", "sqlserver":
		return "SELECT OBJECT_NAME(fkc.parent_object_id), COL_NAME(fkc.parent_object_id, fkc.parent_column_id), " +
			"OBJECT_NAME(fkc.referenced_object_id), COL_NAME(fkc.referenced_object_id, fkc.referenced_column_id) " +
			"FROM sys.foreign_key_columns fkc"
	case "oracle":
		return "SELECT c.table_name, cc.column_name, rc.table_name, rcc.column_name " +
			"FROM all_constraints c " +
			"JOIN all_cons_columns cc ON c.owner = cc.owner AND c.constraint_name = cc.constraint_name " +
			"JOIN all_constraints rc ON c.r_owner = rc.owner AND c.r_constraint_name = rc.constraint_name " +
			"JOIN all_cons_columns rcc ON rc.owner = rcc.owner AND rc.constraint_name = rcc.constraint_name AND cc.position = rcc.position " +
			"WHERE c.constraint_type = 'R'"
	default:
		return ""
	}
}

// fetchERForeignKeys reads the driver's FK metadata; empty where unavailable.
// SQLite exposes foreign keys per table via PRAGMA, so it iterates the given
// table names; every other engine answers a single catalog query.
func fetchERForeignKeys(sess db.Session, driver string, tables []string) []erFK {
	get := func(r []*string, i int) string {
		if i < len(r) && r[i] != nil {
			return *r[i]
		}
		return ""
	}
	if driver == "sqlite" {
		var out []erFK
		for _, t := range tables {
			res, err := sess.Query("PRAGMA foreign_key_list("+sqlitePragmaArg(t)+")", 200)
			if err != nil {
				continue
			}
			for _, r := range res.StringRows() {
				// PRAGMA foreign_key_list columns: id, seq, table, from, to, ...
				if refTable := get(r, 2); refTable != "" {
					out = append(out, erFK{From: t, FromCol: get(r, 3), To: refTable, ToCol: get(r, 4)})
				}
			}
		}
		return out
	}

	q := erFKQuery(driver)
	if q == "" {
		return nil
	}
	res, err := sess.Query(q, 500)
	if err != nil {
		return nil
	}
	var out []erFK
	for _, r := range res.StringRows() {
		if f, fc, t, tc := get(r, 0), get(r, 1), get(r, 2), get(r, 3); f != "" && t != "" {
			out = append(out, erFK{From: f, FromCol: fc, To: t, ToCol: tc})
		}
	}
	return out
}

// sqlitePragmaArg single-quotes a table name for PRAGMA foreign_key_list(...),
// escaping embedded quotes.
func sqlitePragmaArg(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

// openERDiagram fetches the model in the background and opens its window.
func (ws *workspace) openERDiagram() {
	if ws.sess == nil || ws.currentDB == "" {
		showInfo(ws.window, "ER Diagram", "Pick a database first (Ctrl+K).")
		return
	}
	sess := ws.sess
	// Snapshot: the worker must not read ws.conn (commitGated rule).
	driver := ws.conn.Driver
	gen := ws.nextGen()
	safeGo("erd", func() {
		m, err := ws.fetchERModel(sess, driver)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "ER Diagram", "Could not build the diagram:\n"+err.Error())
				return
			}
			if len(m.Tables) == 0 {
				showInfo(ws.window, "ER Diagram", "No tables in this database.")
				return
			}
			ws.showERWindow(m)
		})
	})
}

// showERWindow opens the diagram in a dedicated window with a Reset button.
func (ws *workspace) showERWindow(m *erModel) {
	diagram := newERDiagram(m)
	reset := newFlatButton("Reset Layout")
	reset.onTap = func() {
		layoutERModel(m)
		diagram.applyPositions()
	}
	// Top bar: just the reset action. Bottom status: counts + usage hint.
	toolbar := container.NewBorder(nil, nil, container.NewPadded(reset), nil, nil)
	status := widget.NewLabel(fmt.Sprintf("%d 张表 · %d 个外键 · 拖动表格移动，● 为主键，连线为外键",
		len(m.Tables), len(m.FKs)))
	status.Importance = widget.LowImportance
	bottom := container.NewPadded(status)

	app := fyne.CurrentApp()
	w := app.NewWindow(fmt.Sprintf("ER — %s", ws.currentDB))
	w.Resize(fyne.NewSize(920, 640))
	erdBG := newPanelBackground(diagram, func() color.Color { return pal().contentBG })
	w.SetContent(container.NewBorder(toolbar, bottom, nil, nil, erdBG))
	w.Show()
}

// ---- diagram widget ----------------------------------------------------------

// erDiagram is the scrollable canvas of table nodes + FK lines.
type erDiagram struct {
	widget.BaseWidget
	model    *erModel
	nodes    map[string]*erNodeWidget
	tableIdx map[string]int // name -> index into model.Tables, for O(1) position lookups
	lines    []*canvas.Line
	world    *fyne.Container // nil-layout absolute container
	scroller fyne.CanvasObject
}

func newERDiagram(m *erModel) *erDiagram {
	d := &erDiagram{model: m, nodes: map[string]*erNodeWidget{}, tableIdx: map[string]int{}}
	d.ExtendBaseWidget(d)

	d.world = fyne.NewContainerWithoutLayout() // children keep Move() positions
	for i := range m.Tables {
		node := newERNode(d, m.Tables[i].Name)
		if node == nil {
			continue
		}
		d.nodes[m.Tables[i].Name] = node
		d.tableIdx[m.Tables[i].Name] = i
		d.world.AddObject(node)
	}
	for range m.FKs {
		ln := canvas.NewLine(pal().treeSel)
		d.lines = append(d.lines, ln)
		d.world.AddObject(ln)
	}
	// Fixed world extent so the scroller shows sensible bounds.
	worldW, worldH := float32(1200), float32(1000)
	for _, n := range d.nodes {
		p := n.Position()
		if p.X+n.width > worldW {
			worldW = p.X + n.width
		}
		if p.Y+n.height > worldH {
			worldH = p.Y + n.height
		}
	}
	floor := canvas.NewRectangle(color.Transparent)
	floor.Resize(fyne.NewSize(worldW, worldH))
	d.world.AddObject(floor)

	inner := container.NewGridWrap(fyne.NewSize(worldW, worldH), d.world)
	d.scroller = container.NewHScroll(container.NewVScroll(inner))
	d.applyPositions()
	return d
}

func (d *erDiagram) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.scroller)
}

// applyPositions moves every node and re-lays every FK line.
func (d *erDiagram) applyPositions() {
	// tableIdx keeps the lookup O(1): the naive scan made each drag tick
	// O(tables²) with the FK line relayout on top.
	for name, n := range d.nodes {
		if i, ok := d.tableIdx[name]; ok {
			n.moveTo(d.model.Tables[i].Pos)
		}
	}
	d.layoutLines()
}

// layoutLines recomputes all FK endpoints from the current node positions.
func (d *erDiagram) layoutLines() {
	for i, fk := range d.model.FKs {
		a, b := d.nodes[fk.From], d.nodes[fk.To]
		if a == nil || b == nil {
			continue
		}
		ln := d.lines[i]
		// source: right edge of the from-box at the FK column row
		ln.Position1 = fyne.NewPos(a.Position().X+a.width, a.Position().Y+erdTitleH+float32(a.colIndex(fk.FromCol))*erdRowH)
		ln.Position2 = fyne.NewPos(b.Position().X, b.Position().Y+erdTitleH+float32(b.colIndex(fk.ToCol))*erdRowH)
		ln.Refresh()
	}
}

// erNodeWidget is one table box: border + header + column rows, draggable.
type erNodeWidget struct {
	widget.BaseWidget
	d        *erDiagram
	name     string
	colIdx   map[string]int // column name -> row index (FK endpoint math)
	border   *canvas.Rectangle
	head     *canvas.Rectangle
	title    *canvas.Text
	colTexts []*canvas.Text
	width    float32
	height   float32
}

func newERNode(d *erDiagram, name string) *erNodeWidget {
	var t *erTable
	for i := range d.model.Tables {
		if d.model.Tables[i].Name == name {
			t = &d.model.Tables[i]
			break
		}
	}
	if t == nil {
		return nil
	}
	sz := erBoxSize(*t)

	n := &erNodeWidget{d: d, name: name, width: sz.Width, height: sz.Height}
	// Column-name → row index, so the FK endpoint math never rescans the
	// column list per line per drag tick.
	n.colIdx = make(map[string]int, len(t.Cols))
	for i, c := range t.Cols {
		n.colIdx[c.Name] = i
	}
	p := pal()
	n.border = canvas.NewRectangle(p.contentBG)
	n.border.StrokeColor = p.line
	n.border.StrokeWidth = 1
	n.border.CornerRadius = 4
	n.border.Resize(sz)

	n.head = canvas.NewRectangle(p.gridHeaderBG)
	n.head.CornerRadius = 4
	n.head.Resize(fyne.NewSize(sz.Width, erdTitleH))

	n.title = canvas.NewText(name, p.gridHeaderFG)
	n.title.TextSize = 12
	n.title.TextStyle.Bold = true
	n.title.Move(fyne.NewPos(6, 4))
	n.title.Resize(fyne.NewSize(sz.Width-12, 16))

	for _, c := range t.Cols {
		label := c.Name
		if c.PK {
			label = "● " + label
		}
		ct := canvas.NewText(label, p.gridCellFG)
		ct.TextSize = 11
		n.colTexts = append(n.colTexts, ct)
	}

	n.ExtendBaseWidget(n)
	n.moveTo(t.Pos)
	return n
}

func (n *erNodeWidget) CreateRenderer() fyne.WidgetRenderer {
	objs := []fyne.CanvasObject{n.border, n.head, n.title}
	for i, ct := range n.colTexts {
		ct.Move(fyne.NewPos(10, erdTitleH+float32(i)*erdRowH+1))
		ct.Resize(fyne.NewSize(n.width-16, erdRowH-1))
		objs = append(objs, ct)
	}
	return widget.NewSimpleRenderer(container.NewStack(objs...))
}

func (n *erNodeWidget) MinSize() fyne.Size {
	return fyne.NewSize(n.width, n.height)
}

// moveTo repositions the widget; its internal objects are renderer-stacked at
// fixed offsets, so moving the widget moves everything.
func (n *erNodeWidget) moveTo(pos fyne.Position) {
	n.Move(pos)
}

// colIndex finds a column's row index within the node (0 when unknown).
func (n *erNodeWidget) colIndex(col string) int {
	if i, ok := n.colIdx[col]; ok {
		return i
	}
	return 0
}

// ---- dragging ----------------------------------------------------------------

// Dragged moves the node and re-lays the FK lines (TablePlus "drag to move").
func (n *erNodeWidget) Dragged(e *fyne.DragEvent) {
	pos := fyne.NewPos(n.Position().X+e.Dragged.DX, n.Position().Y+e.Dragged.DY)
	n.moveTo(pos)
	// tableIdx (not a scan over model.Tables): drag ticks fire per pixel, and
	// the naive scan made each tick O(tables) with the FK relayout on top.
	if i, ok := n.d.tableIdx[n.name]; ok {
		n.d.model.Tables[i].Pos = pos
	}
	n.d.layoutLines()
}

// DragEnd is a no-op (positions are committed on the fly).
func (n *erNodeWidget) DragEnd() {}

func (n *erNodeWidget) Cursor() desktop.Cursor { return desktop.PointerCursor }

// ColumnsByName helper on the model.
func (m *erModel) ColumnsByName(name string) []erCol {
	for i := range m.Tables {
		if m.Tables[i].Name == name {
			return m.Tables[i].Cols
		}
	}
	return nil
}
