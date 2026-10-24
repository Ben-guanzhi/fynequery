package main

// Object DDL retrieval (the "pure DDL editing route"): views expose the
// server's CREATE statement, functions/procedures expose the routine body,
// and ordinary tables fall back to the synthesised CREATE TABLE script.
// Opened in a read-mostly editor tab where the user can copy or adapt.

import (
	"fmt"
	"strings"

	"fynequery/db"
	"fynequery/proto"
)

// fetchObjectDDL returns the DDL text for a database object. kind is "table",
// "view" or "function" (from the sidebar inventory). Best effort: an error
// means the dialect does not expose the definition (e.g. MSSQL functions).
func fetchObjectDDL(sess db.Session, driver, kind, name string) (string, error) {
	switch kind {
	case "view":
		return viewDDL(sess, driver, name)
	case "function":
		return functionDDL(sess, driver, name)
	default:
		return tableDDL(sess, driver, name)
	}
}

// viewDDL returns the CREATE VIEW statement for a view.
func viewDDL(sess db.Session, driver, name string) (string, error) {
	q := ""
	switch driver {
	case "mysql", "mariadb":
		q = "SELECT CONCAT('CREATE OR REPLACE VIEW `" + sqlQuote.Replace(name) + "' AS\n', IFNULL(VIEW_DEFINITION,''), ';') " +
			"FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '" +
			sqlQuote.Replace(name) + "'"
	case "postgres", "postgresql":
		q = "SELECT 'CREATE OR REPLACE VIEW ' || current_schema() || '.' || '" +
			sqlQuote.Replace(name) + "' AS\n' || view_definition || ';' " +
			"FROM information_schema.views WHERE table_schema = current_schema() AND table_name = '" +
			sqlQuote.Replace(name) + "'"
	case "mssql":
		q = "SELECT m.definition FROM sys.sql_modules m JOIN sys.objects o ON o.object_id = m.object_id " +
			"WHERE o.type = 'V' AND o.name = N'" + strings.ReplaceAll(name, "'", "''") + "'"
	default:
		return "", fmt.Errorf("view DDL not supported for %s", driver)
	}
	res, err := sess.Query(q, 1)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || res.Rows[0][0].Kind == "null" {
		return "", fmt.Errorf("no view definition found for %s", name)
	}
	return strings.TrimSpace(proto.CellText(res.Rows[0][0])), nil
}

// functionDDL returns the routine body for a function/procedure.
func functionDDL(sess db.Session, driver, name string) (string, error) {
	q := ""
	switch driver {
	case "mysql", "mariadb":
		q = "SELECT ROUTINE_DEFINITION FROM information_schema.ROUTINES " +
			"WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_NAME = '" + sqlQuote.Replace(name) + "' LIMIT 1"
	case "postgres", "postgresql":
		q = "SELECT pg_get_functiondef(oid) FROM pg_proc p " +
			"WHERE p.proname = '" + sqlQuote.Replace(name) + "' LIMIT 1"
	case "mssql":
		q = "SELECT OBJECT_DEFINITION(OBJECT_ID(N'" + strings.ReplaceAll(name, "'", "''") + "'))"
	default:
		return "", fmt.Errorf("routine DDL not supported for %s", driver)
	}
	res, err := sess.Query(q, 1)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || res.Rows[0][0].Kind == "null" {
		return "", fmt.Errorf("no routine definition found for %s", name)
	}
	body := strings.TrimSpace(proto.CellText(res.Rows[0][0]))
	return fmt.Sprintf("-- definition of %s (editable; re-run to apply)\n%s;\n", name, body), nil
}

// tableDDL synthesises a CREATE TABLE script from the live column metadata.
func tableDDL(sess db.Session, driver, name string) (string, error) {
	cols, err := sess.Columns(name)
	if err != nil {
		return "", err
	}
	return buildCreateScript(name, quoteTableFor(driver, name), cols, driver), nil
}

// quoteTableFor quotes an identifier for the driver (backticks for MySQL,
// double quotes otherwise).
func quoteTableFor(driver, name string) string {
	return quoteCol(name, driver)
}
