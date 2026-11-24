package dbintel

import (
	"context"
	"database/sql"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInspectReadsSchemaOnly(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	schema, err := Inspect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Tables) != 1 || len(schema.Tables[0].Columns) != 2 {
		t.Fatalf("schema=%+v", schema)
	}
}

func TestInspectHandlesNullableSQLiteMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{name: "implicit foreign key target", sql: `CREATE TABLE parents(id INTEGER PRIMARY KEY); CREATE TABLE children(parent_id INTEGER REFERENCES parents)`},
		{name: "expression index", sql: `CREATE TABLE children(name TEXT, parent_id INTEGER); CREATE INDEX children_name_idx ON children(lower(name), parent_id)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, err := sql.Open("sqlite3", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			database.SetMaxOpenConns(1)
			defer database.Close()
			if _, err := database.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			schema, err := Inspect(context.Background(), database)
			if err != nil {
				t.Fatalf("inspect valid SQLite schema: %v", err)
			}
			children := schema.Tables[0]
			if children.Name != "children" {
				t.Fatalf("unexpected table: %+v", children)
			}
			if tc.name == "implicit foreign key target" {
				want := []ForeignKey{{Column: "parent_id", ReferencedTable: "parents", ReferencedColumn: ""}}
				if !reflect.DeepEqual(children.ForeignKeys, want) {
					t.Fatalf("foreign keys = %+v, want %+v", children.ForeignKeys, want)
				}
			} else if len(children.Indexes) != 1 || !reflect.DeepEqual(children.Indexes[0].Columns, []string{"", "parent_id"}) {
				t.Fatalf("expression index positions were lost: %+v", children.Indexes)
			}
		})
	}
}

func TestInspectWorkspaceIgnoresSymlinkedMigrations(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	outside := filepath.Join(t.TempDir(), "outside.sql")
	if err := os.WriteFile(outside, []byte("CREATE TABLE outside(id INTEGER);"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "migration.sql")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	schema, err := InspectWorkspace(context.Background(), database, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Migrations) != 0 {
		t.Fatalf("scanned migration outside workspace: %+v", schema.Migrations)
	}
}

func TestInspectWorkspaceReportsRelationshipsAndMigrations(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE posts(id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id)); CREATE INDEX posts_user_idx ON posts(user_id)`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "001_users.sql"), []byte("CREATE TABLE users(id INTEGER PRIMARY KEY); ALTER TABLE posts ADD user_id INTEGER;"), 0o600); err != nil {
		t.Fatal(err)
	}
	schema, err := InspectWorkspace(context.Background(), database, root)
	if err != nil {
		t.Fatal(err)
	}
	var posts Table
	for _, table := range schema.Tables {
		if table.Name == "posts" {
			posts = table
		}
	}
	if len(posts.ForeignKeys) != 1 || posts.ForeignKeys[0].ReferencedTable != "users" || len(posts.Indexes) != 1 {
		t.Fatalf("relationship metadata=%+v", posts)
	}
	if len(schema.Migrations) != 1 || len(schema.Migrations[0].Tables) != 2 {
		t.Fatalf("migration metadata=%+v", schema.Migrations)
	}
}

func TestReferencedTablesSkipsSQLControlWords(t *testing.T) {
	got := referencedTables(`CREATE TABLE IF NOT EXISTS users(id INTEGER);
CREATE TRIGGER IF NOT EXISTS update_users AFTER UPDATE ON users BEGIN UPDATE users SET id = id WHERE id = new.id; END;
CREATE INDEX IF NOT EXISTS users_id_idx ON users(id);`)
	want := []string{"users"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("referenced tables=%v want=%v", got, want)
	}
}
