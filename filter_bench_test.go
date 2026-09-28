package main

import (
	"fmt"
	"testing"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// buildWideTree fills a root>db>8 schemas>250 tables tree (2009 nodes) and
// returns the node count.
func buildWideTree(c *Client, schemas, perSchema int) int {
	root := &nodeData{kind: "root", label: "数据库", uid: treeRootUID}
	c.treeNodes[treeRootUID] = root
	visited := 1
	dbUID := "db:d"
	root.children = append(root.children, dbUID)
	c.treeNodes[dbUID] = &nodeData{kind: "db", label: "d", db: "d", uid: dbUID}
	visited++
	for s := 0; s < schemas; s++ {
		schUID := fmt.Sprintf("%s|s:s%d", dbUID, s)
		c.treeNodes[dbUID].children = append(c.treeNodes[dbUID].children, schUID)
		c.treeNodes[schUID] = &nodeData{kind: "schema", label: fmt.Sprintf("s%d", s), db: "d", schema: fmt.Sprintf("s%d", s), uid: schUID}
		visited++
		for t := 0; t < perSchema; t++ {
			tUID := fmt.Sprintf("%s|t:tbl%d", schUID, t)
			c.treeNodes[tUID] = &nodeData{kind: "table", label: fmt.Sprintf("tbl%d", t), db: "d", schema: fmt.Sprintf("s%d", s), name: fmt.Sprintf("tbl%d", t), uid: tUID}
			c.treeNodes[schUID].children = append(c.treeNodes[schUID].children, tUID)
			visited++
		}
	}
	return visited
}

// BenchmarkFilterRebuild measures the steady-state cost of a keystroke in
// the filter box on a fully loaded 2009-node tree (the filter matches, so a
// rebuild renders every branch). Row-widget memoization plus the identical
// list check keep each keystroke at walk+compare cost instead of paying
// fyne's re-render of 2009 rows (measured ~880ms before memoization).
func BenchmarkFilterRebuild(b *testing.B) {
	test.NewApp()
	c := &Client{store: db.NewStore(), treeNodes: make(map[string]*nodeData), expanded: make(map[string]bool)}
	c.schemaRows = container.NewVBox()
	n := buildWideTree(c, 8, 250)
	c.treeFilter = "tbl"
	c.rebuildSchemaRows() // warm: real keystrokes arrive on a rendered tree
	b.ReportMetric(float64(n), "nodes")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.rebuildSchemaRows()
	}
}

// BenchmarkFilterRebuildChanging types real filter text: every keystroke
// narrows the visible set, so rows must actually be swapped and fyne
// re-lays the list; memoization still saves re-creating the widgets that
// survive (their key is unchanged).
func BenchmarkFilterRebuildChanging(b *testing.B) {
	test.NewApp()
	c := &Client{store: db.NewStore(), treeNodes: make(map[string]*nodeData), expanded: make(map[string]bool)}
	c.schemaRows = container.NewVBox()
	n := buildWideTree(c, 8, 250)
	filters := []string{"t", "tb", "tbl", "tbl1", "tbl12", "tbl123"}
	c.rebuildSchemaRows()
	b.ReportMetric(float64(n), "nodes")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.treeFilter = filters[i%len(filters)]
		c.rebuildSchemaRows()
	}
}
