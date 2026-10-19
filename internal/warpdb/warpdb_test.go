package warpdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReadCommittedWALAndNestedLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Warp database.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE windows(id INTEGER, active_tab_index INTEGER)`,
		`CREATE TABLE tabs(id INTEGER, window_id INTEGER, custom_title TEXT, color TEXT, tab_group_id INTEGER)`,
		`CREATE TABLE tab_groups(id INTEGER, name TEXT)`,
		`CREATE TABLE pane_nodes(id INTEGER, tab_id INTEGER, parent_pane_node_id INTEGER, flex FLOAT, is_leaf BOOLEAN)`,
		`CREATE TABLE terminal_panes(id INTEGER, uuid BLOB, cwd TEXT)`,
		`CREATE TABLE pane_leaves(pane_node_id INTEGER, is_focused BOOLEAN)`,
		`CREATE TABLE pane_branches(pane_node_id INTEGER, horizontal BOOLEAN)`,
		`INSERT INTO windows VALUES(1,0)`,
		`INSERT INTO tabs VALUES(10,1,'Agents','blue',1)`,
		`INSERT INTO tab_groups VALUES(1,'Work')`,
		`INSERT INTO pane_nodes VALUES(100,10,NULL,1,0),(101,10,100,0.5,1),(102,10,100,0.5,1)`,
		`INSERT INTO pane_branches VALUES(100,1)`,
		`INSERT INTO terminal_panes VALUES(101,X'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA','/tmp/first'),(102,X'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB','/tmp/second')`,
		`INSERT INTO pane_leaves VALUES(101,1),(102,0)`,
	}
	for _, s := range statements {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	// Keep an uncommitted change open while reading the live WAL database.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE tabs SET custom_title='uncommitted'`); err != nil {
		t.Fatal(err)
	}
	l, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Windows) != 1 || len(l.Panes()) != 2 {
		t.Fatalf("unexpected layout: %+v", l)
	}
	tab := l.Windows[0].Tabs[0]
	if tab.Title != "Agents" || tab.Group != "Work" || !tab.Root.Horizontal {
		t.Fatalf("inconsistent layout: %+v", tab)
	}
	if p := l.Panes()[0]; p.UUID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || !p.IsFocused || p.CWD != "/tmp/first" {
		t.Fatalf("bad pane: %+v", p)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE pane_leaves ADD COLUMN custom_vertical_tabs_title TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pane_leaves SET custom_vertical_tabs_title='重命名 pane' WHERE pane_node_id=101`); err != nil {
		t.Fatal(err)
	}
	l, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Panes()[0].Title != "重命名 pane" || l.Panes()[1].Title != "" {
		t.Fatal("pane labels lost")
	}
}
