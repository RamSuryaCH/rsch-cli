// Package dbintel provides read-only database schema inspection for planning
// and verification. It never executes writes or arbitrary user SQL.
package dbintel

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	Default    any    `json:"default,omitempty"`
	PrimaryKey bool   `json:"primaryKey"`
}
type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
}

type ForeignKey struct {
	Column          string `json:"column"`
	ReferencedTable string `json:"referencedTable"`
	// ReferencedColumn is empty when SQLite implicitly targets the parent primary key.
	ReferencedColumn string `json:"referencedColumn"`
}

type Index struct {
	Name   string `json:"name"`
	Unique bool   `json:"unique"`
	// Columns preserves index order; an empty entry denotes an expression or rowid.
	Columns []string `json:"columns"`
}

type MigrationReference struct {
	Path   string   `json:"path"`
	Tables []string `json:"tables,omitempty"`
}

type Schema struct {
	Tables     []Table              `json:"tables"`
	Migrations []MigrationReference `json:"migrations,omitempty"`
}

func Inspect(ctx context.Context, db *sql.DB) (Schema, error) {
	if db == nil {
		return Schema{}, fmt.Errorf("database is required")
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return Schema{}, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return Schema{}, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return Schema{}, err
	}
	if err := rows.Err(); err != nil {
		return Schema{}, err
	}
	schema := Schema{Tables: make([]Table, 0, len(names))}
	for _, name := range names {
		columns, err := inspectColumns(ctx, db, name)
		if err != nil {
			return Schema{}, err
		}
		foreignKeys, err := inspectForeignKeys(ctx, db, name)
		if err != nil {
			return Schema{}, err
		}
		indexes, err := inspectIndexes(ctx, db, name)
		if err != nil {
			return Schema{}, err
		}
		schema.Tables = append(schema.Tables, Table{Name: name, Columns: columns, ForeignKeys: foreignKeys, Indexes: indexes})
	}
	return schema, nil
}

// InspectWorkspace adds migration-file provenance to the read-only schema
// report. SQL is treated as text for relationship discovery; it is never
// executed or interpolated into a database query.
func InspectWorkspace(ctx context.Context, db *sql.DB, root string) (Schema, error) {
	schema, err := Inspect(ctx, db)
	if err != nil {
		return Schema{}, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Schema{}, err
	}
	references := make([]MigrationReference, 0)
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			base := filepath.Base(path)
			if path != absolute && (base == ".git" || base == ".rsch" || base == "node_modules" || base == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) != ".sql" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tables := referencedTables(string(data))
		if len(tables) == 0 {
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		references = append(references, MigrationReference{Path: filepath.ToSlash(relative), Tables: tables})
		return nil
	})
	if err != nil {
		return Schema{}, err
	}
	sort.Slice(references, func(i, j int) bool { return references[i].Path < references[j].Path })
	schema.Migrations = references
	return schema, nil
}

func inspectForeignKeys(ctx context.Context, db *sql.DB, table string) ([]ForeignKey, error) {
	rows, err := db.QueryContext(ctx, `SELECT "from","table","to" FROM pragma_foreign_key_list(?) ORDER BY id,seq`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ForeignKey, 0)
	for rows.Next() {
		var item ForeignKey
		var referencedColumn sql.NullString
		if err := rows.Scan(&item.Column, &item.ReferencedTable, &referencedColumn); err != nil {
			return nil, err
		}
		item.ReferencedColumn = referencedColumn.String
		items = append(items, item)
	}
	return items, rows.Err()
}

func inspectIndexes(ctx context.Context, db *sql.DB, table string) ([]Index, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,"unique" FROM pragma_index_list(?) ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	items := make([]Index, 0)
	for rows.Next() {
		var name string
		var unique int
		if err := rows.Scan(&name, &unique); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, Index{Name: name, Unique: unique == 1})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		columns, err := indexColumns(ctx, db, items[i].Name)
		if err != nil {
			return nil, err
		}
		items[i].Columns = columns
	}
	return items, nil
}

func indexColumns(ctx context.Context, db *sql.DB, name string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var column sql.NullString
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column.String)
	}
	return columns, rows.Err()
}

var tableReferencePattern = regexp.MustCompile(`(?i)\b(?:table|into|update|from|join|references)\s+(?:(?:if\s+)?(?:not\s+)?exists\s+)?([a-zA-Z_][a-zA-Z0-9_]*)`)
var onTableReferencePattern = regexp.MustCompile(`(?i)\b(?:after\s+(?:insert|update|delete)\s+on|create\s+(?:unique\s+)?index\b[^;]*\bon)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)

var tableReferenceKeywords = map[string]bool{
	"begin": true, "conflict": true, "delete": true, "end": true,
	"exists": true, "if": true, "insert": true, "not": true,
	"on": true, "select": true, "set": true, "table": true,
	"update": true, "where": true,
}

func referencedTables(sqlText string) []string {
	seen := map[string]bool{}
	add := func(matches [][]string) {
		for _, match := range matches {
			if len(match) > 1 {
				name := match[1]
				if !tableReferenceKeywords[strings.ToLower(name)] {
					seen[name] = true
				}
			}
		}
	}
	add(tableReferencePattern.FindAllStringSubmatch(sqlText, -1))
	add(onTableReferencePattern.FindAllStringSubmatch(sqlText, -1))
	tables := make([]string, 0, len(seen))
	for table := range seen {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}
func inspectColumns(ctx context.Context, db *sql.DB, name string) ([]Column, error) {
	rows, err := db.QueryContext(ctx, `SELECT cid,name,type,"notnull",dflt_value,pk FROM pragma_table_info(?)`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []Column
	for rows.Next() {
		var cid int
		var column, typ string
		var notNull, primary int
		var defaultValue any
		if err := rows.Scan(&cid, &column, &typ, &notNull, &defaultValue, &primary); err != nil {
			return nil, err
		}
		columns = append(columns, Column{Name: column, Type: typ, Nullable: notNull == 0, Default: defaultValue, PrimaryKey: primary > 0})
	}
	return columns, rows.Err()
}
