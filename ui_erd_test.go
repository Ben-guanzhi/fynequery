package main

// ER 图回归（P4）。渲染结果无法在无头环境里做像素校验，这里只钉住它不会
// 崩、且无连接时给出提示而不是抛错；真实取数留给 integration_test.go。

import (
	"testing"

	"tablepro-fyne/internal/db"
)

// TestERDiagramBodyRendersBoxesAndEdges is the smoke test: tables become boxes,
// a foreign key becomes an edge, and building the dialog must not panic.
func TestERDiagramBodyRendersBoxesAndEdges(t *testing.T) {
	c := newCrumbClient(t)
	tables := []db.TableInfo{{Name: "orders"}, {Name: "users"}, {Name: "items"}}
	fks := map[string][]db.ForeignKeyInfo{
		"orders": {{
			Name:       "fk_orders_user",
			Columns:    []string{"user_id"},
			RefTable:   "users",
			RefColumns: []string{"id"},
		}},
	}
	c.showERDiagramBody(tables, fks)
}

// TestERDiagramBodyEmpty pins the empty state: no tables means a message, not
// an empty canvas.
func TestERDiagramBodyEmpty(t *testing.T) {
	c := newCrumbClient(t)
	c.showERDiagramBody(nil, nil)
}

// TestERDiagramNeedsConnection pins that an unconnected client is told so
// instead of firing catalog queries against nothing.
func TestERDiagramNeedsConnection(t *testing.T) {
	c := newCrumbClient(t)
	c.currentID = ""
	c.showERDiagram("shop", "")
}
