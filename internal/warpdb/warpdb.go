// Package warpdb reads the window, tab and pane layout out of Warp's sqlite
// database in a read-only transaction, including its write-ahead log.
package warpdb

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Pane is one terminal pane, the leaf of a tab's split tree.
type Pane struct {
	NodeID    int
	UUID      string // lowercase hex, matches WARP_TERMINAL_SESSION_UUID
	CWD       string
	Title     string `json:",omitempty"`
	IsFocused bool
}

// Node is a node in a tab's split tree: either a leaf pane or a branch.
type Node struct {
	Flex float64
	// Horizontal is set on branches and describes how children are arranged.
	Horizontal bool
	// Pane is set on leaves.
	Pane     *Pane
	Children []*Node
}

// IsLeaf reports whether the node holds a pane.
func (n *Node) IsLeaf() bool { return n.Pane != nil }

// Tab is one Warp tab.
type Tab struct {
	ID    int
	Title string
	Color string
	Group string
	Root  *Node
}

// Window is one Warp window.
type Window struct {
	ID             int
	ActiveTabIndex int
	Tabs           []Tab
}

// Layout is the full persisted layout.
type Layout struct {
	Windows []Window
}

// Panes returns every pane in the layout.
func (l *Layout) Panes() []*Pane {
	var out []*Pane
	for _, w := range l.Windows {
		for _, t := range w.Tabs {
			collectPanes(t.Root, &out)
		}
	}
	return out
}

func collectPanes(n *Node, out *[]*Pane) {
	if n == nil {
		return
	}
	if n.IsLeaf() {
		*out = append(*out, n.Pane)
		return
	}
	for _, c := range n.Children {
		collectPanes(c, out)
	}
}

// Read loads the layout from Warp's database.
func Read(dbPath string) (*Layout, error) {
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(3000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("read Warp database: %w", err)
	}
	defer tx.Rollback()

	panes, err := readPanes(tx)
	if err != nil {
		return nil, err
	}
	nodesByTab, err := readNodes(tx, panes)
	if err != nil {
		return nil, err
	}
	return readWindows(tx, nodesByTab)
}

type rawNode struct {
	id       int
	tabID    int
	parent   sql.NullInt64
	flex     sql.NullFloat64
	isLeaf   bool
	node     *Node
	children []*rawNode
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func readPanes(db queryer) (map[int]*Pane, error) {
	const query = `
		SELECT tp.id, lower(hex(tp.uuid)), COALESCE(tp.cwd, ''),
		       COALESCE(pl.is_focused, 0), %s
		FROM terminal_panes tp
		LEFT JOIN pane_leaves pl ON pl.pane_node_id = tp.id`
	rows, err := db.Query(fmt.Sprintf(query, "COALESCE(pl.custom_vertical_tabs_title, '')"))
	if err != nil && strings.Contains(err.Error(), "no such column: pl.custom_vertical_tabs_title") {
		// Older Warp databases predate custom per-pane sidebar labels.
		rows, err = db.Query(fmt.Sprintf(query, "''"))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int]*Pane{}
	for rows.Next() {
		var p Pane
		var uuid string
		if err := rows.Scan(&p.NodeID, &uuid, &p.CWD, &p.IsFocused, &p.Title); err != nil {
			return nil, err
		}
		p.UUID = normalizeUUID(uuid)
		out[p.NodeID] = &p
	}
	return out, rows.Err()
}

// normalizeUUID strips dashes and lowercases, so database and environment
// values compare equal.
func normalizeUUID(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

func readNodes(db queryer, panes map[int]*Pane) (map[int][]*rawNode, error) {
	branches := map[int]bool{}
	brows, err := db.Query(`SELECT pane_node_id, horizontal FROM pane_branches`)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var id int
		var horizontal bool
		if err := brows.Scan(&id, &horizontal); err != nil {
			return nil, err
		}
		branches[id] = horizontal
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, tab_id, parent_pane_node_id, flex, is_leaf
		FROM pane_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := map[int]*rawNode{}
	byTab := map[int][]*rawNode{}
	for rows.Next() {
		var r rawNode
		if err := rows.Scan(&r.id, &r.tabID, &r.parent, &r.flex, &r.isLeaf); err != nil {
			return nil, err
		}
		n := &Node{Horizontal: branches[r.id]}
		if r.flex.Valid {
			n.Flex = r.flex.Float64
		}
		if r.isLeaf {
			n.Pane = panes[r.id]
		}
		r.node = n
		rn := r
		byID[r.id] = &rn
		byTab[r.tabID] = append(byTab[r.tabID], &rn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Link children to parents in id order, which is the order Warp displays them.
	for _, r := range byTab {
		for _, n := range r {
			if !n.parent.Valid {
				continue
			}
			if p, ok := byID[int(n.parent.Int64)]; ok {
				p.children = append(p.children, n)
				p.node.Children = append(p.node.Children, n.node)
			}
		}
	}
	return byTab, nil
}

func readWindows(db queryer, nodesByTab map[int][]*rawNode) (*Layout, error) {
	groups := map[int]string{}
	grows, err := db.Query(`SELECT id, COALESCE(name, '') FROM tab_groups`)
	if err == nil {
		defer grows.Close()
		for grows.Next() {
			var id int
			var name string
			if err := grows.Scan(&id, &name); err == nil {
				groups[id] = name
			}
		}
	}

	rows, err := db.Query(`
		SELECT t.id, t.window_id, COALESCE(t.custom_title, ''), COALESCE(t.color, ''),
		       COALESCE(t.tab_group_id, 0)
		FROM tabs t ORDER BY t.window_id, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tabsByWindow := map[int][]Tab{}
	for rows.Next() {
		var t Tab
		var windowID, groupID int
		if err := rows.Scan(&t.ID, &windowID, &t.Title, &t.Color, &groupID); err != nil {
			return nil, err
		}
		t.Group = groups[groupID]
		for _, n := range nodesByTab[t.ID] {
			if !n.parent.Valid {
				t.Root = n.node
				break
			}
		}
		if t.Root == nil {
			// A tab with no pane tree has nothing to restore.
			continue
		}
		tabsByWindow[windowID] = append(tabsByWindow[windowID], t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	wrows, err := db.Query(`SELECT id, active_tab_index FROM windows ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer wrows.Close()

	var layout Layout
	for wrows.Next() {
		var w Window
		if err := wrows.Scan(&w.ID, &w.ActiveTabIndex); err != nil {
			return nil, err
		}
		w.Tabs = tabsByWindow[w.ID]
		sort.SliceStable(w.Tabs, func(i, j int) bool { return w.Tabs[i].ID < w.Tabs[j].ID })
		layout.Windows = append(layout.Windows, w)
	}
	return &layout, wrows.Err()
}
