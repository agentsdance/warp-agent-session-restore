package session

import "github.com/agentsdance/warp-agent-session-restore/internal/warpdb"

// Keep the latest layout authoritative and recover only absent pane identities.
// Warp can remove a window/tab from its DB while its agents are still alive.
// Replacing the whole layout would lose newly opened tabs and moved panes.
func retainMissingLayout(previous, next *Snapshot) {
	if previous.Layout == nil || next.Layout == nil {
		return
	}
	present := map[string]bool{}
	for _, p := range next.Layout.Panes() {
		present[p.UUID] = true
	}
	missing := map[string]bool{}
	for _, s := range next.Sessions {
		if !present[s.Pane] {
			missing[s.Pane] = true
		}
	}
	if len(missing) == 0 {
		return
	}
	// Copy slices before appending to ensure the historical snapshot is immutable.
	out := *next.Layout
	out.Windows = append([]warpdb.Window(nil), out.Windows...)
	for i := range out.Windows {
		out.Windows[i].Tabs = append([]warpdb.Tab(nil), out.Windows[i].Tabs...)
	}
	var hasMissing func(*warpdb.Node) bool
	hasMissing = func(n *warpdb.Node) bool {
		if n == nil {
			return false
		}
		if n.IsLeaf() {
			return missing[n.Pane.UUID]
		}
		for _, c := range n.Children {
			if hasMissing(c) {
				return true
			}
		}
		return false
	}
	var absent func(*warpdb.Node) *warpdb.Node
	absent = func(n *warpdb.Node) *warpdb.Node {
		if n == nil {
			return nil
		}
		if n.IsLeaf() {
			if present[n.Pane.UUID] {
				return nil
			}
			present[n.Pane.UUID] = true
			return n
		}
		c := *n
		c.Children = nil
		for _, child := range n.Children {
			if v := absent(child); v != nil {
				c.Children = append(c.Children, v)
			}
		}
		if len(c.Children) == 0 {
			return nil
		}
		if len(c.Children) == 1 {
			return c.Children[0]
		}
		return &c
	}
	for _, w := range previous.Layout.Windows {
		for _, t := range w.Tabs {
			if !hasMissing(t.Root) {
				continue
			}
			root := absent(t.Root)
			if root == nil {
				continue
			}
			wi := -1
			for i := range out.Windows {
				if out.Windows[i].ID == w.ID {
					wi = i
					break
				}
			}
			if wi < 0 {
				nw := w
				nw.Tabs = nil
				out.Windows = append(out.Windows, nw)
				wi = len(out.Windows) - 1
			}
			ti := -1
			for i := range out.Windows[wi].Tabs {
				if out.Windows[wi].Tabs[i].ID == t.ID {
					ti = i
					break
				}
			}
			if ti < 0 {
				nt := t
				nt.Root = root
				out.Windows[wi].Tabs = append(out.Windows[wi].Tabs, nt)
			} else {
				current := out.Windows[wi].Tabs[ti].Root
				if current == nil {
					out.Windows[wi].Tabs[ti].Root = root
				} else {
					out.Windows[wi].Tabs[ti].Root = &warpdb.Node{Horizontal: t.Root.Horizontal, Children: []*warpdb.Node{current, root}}
				}
			}
		}
	}
	next.Layout = &out
}
