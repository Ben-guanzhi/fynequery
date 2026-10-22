package main

// ER diagram (TablePlus "Diagram" read-only slice): a scrollable canvas of
// table boxes joined by straight foreign-key lines. Tables are draggable
// (fyne.Draggable) into place; the diagram opens in its own window.

import (
	"fmt"
	"image/color"
	"sort"

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
func (ws *workspace) fetchERModel(sess db.Session) (*erModel, error) {
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
	m.FKs = fetchERForeignKeys(sess, ws.conn.Driver)
	layoutERModel(m)
	return m, nil
}

// layoutERModel assigns initial grid positions (3 tables per row).
func layoutERModel(m *erModel) {
	for i := range m.Tables {
		sz := erBoxSize(m.Tables[i])
		m.Tables[i].Pos = fyne.NewPos(
			erdGap+float32(i%3)*(erdBoxW+erdGap),
			erdGap+float32(i/3)*(sz.Height+erdGap))
	}
}

// erFKQuery returns the information_schema FK query for a driver, or "" when
// the driver has no diagram support.
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
	default:
		return ""
	}
}

// fetchERForeignKeys reads the driver's FK metadata; empty where unavailable.
func fetchERForeignKeys(sess db.Session, driver string) []erFK {
	q := erFKQuery(driver)
	if q == "" {
		return nil
	}
	res, err := sess.Query(q, 500)
	if err != nil {
		return nil
	}
	var out []erFK
	for _, r := range res.Rows {
		get := func(i int) string {
			if i < len(r) && r[i] != nil {
				return *r[i]
			}
			return ""
		}
		if f, fc, t, tc := get(0), get(1), get(2), get(3); f != "" && t != "" {
			out = append(out, erFK{From: f, FromCol: fc, To: t, ToCol: tc})
		}
	}
	return out
}

// openERDiagram fetches the model in the background and opens its window.
func (ws *workspace) openERDiagram() {
	if ws.sess == nil || ws.currentDB == "" {
		showInfo(ws.window, "ER Diagram", "Pick a database first (Ctrl+K).")
		return
	}
	sess := ws.sess
	gen := ws.nextGen()
	safeGo("erd", func() {
		m, err := ws.fetchERModel(sess)
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
	lines    []*canvas.Line
	world    *fyne.Container // nil-layout absolute container
	scroller fyne.CanvasObject
}

func newERDiagram(m *erModel) *erDiagram {
	d := &erDiagram{model: m, nodes: map[string]*erNodeWidget{}}
	d.ExtendBaseWidget(d)

	d.world = fyne.NewContainerWithoutLayout() // children keep Move() positions
	for i := range m.Tables {
		node := newERNode(d, m.Tables[i].Name)
		if node == nil {
			continue
		}
		d.nodes[m.Tables[i].Name] = node
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
	for name, n := range d.nodes {
		for i := range d.model.Tables {
			if d.model.Tables[i].Name == name {
				n.moveTo(d.model.Tables[i].Pos)
				break
			}
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
	for i, c := range n.d.model.ColumnsByName(n.name) {
		if c.Name == col {
			return i
		}
	}
	return 0
}

// ---- dragging ----------------------------------------------------------------

// Dragged moves the node and re-lays the FK lines (TablePlus "drag to move").
func (n *erNodeWidget) Dragged(e *fyne.DragEvent) {
	pos := fyne.NewPos(n.Position().X+e.Dragged.DX, n.Position().Y+e.Dragged.DY)
	n.moveTo(pos)
	for i := range n.d.model.Tables {
		if n.d.model.Tables[i].Name == n.name {
			n.d.model.Tables[i].Pos = pos
		}
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
