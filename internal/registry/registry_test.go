package registry

import (
	"testing"
	"time"
)

func TestLateEndCannotEraseReplacementSession(t *testing.T) {
	now := time.Now()
	events := []Entry{{Time: now, Event: "start", PaneUUID: "p", SessionID: "old"}, {Time: now.Add(time.Second), Event: "start", PaneUUID: "p", SessionID: "new"}, {Time: now.Add(2 * time.Second), Event: "end", PaneUUID: "p", SessionID: "old"}}
	if got := Current(events)["p"].SessionID; got != "new" {
		t.Fatalf("got %q", got)
	}
	events = append(events, Entry{Time: now.Add(3 * time.Second), Event: "end", PaneUUID: "p", SessionID: "new"})
	if len(Current(events)) != 0 {
		t.Fatal("ended session is still live")
	}
}
