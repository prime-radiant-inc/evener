package server

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// The root row carries its tree's tally, read through the seam when the row is
// listed; a descendant row never does, and a tree with no subagents, or a
// session with no delegate tree, carries none (S3 ruling 22).
func TestThreadListRootRowCarriesItsTreesSubagentTally(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tally appwire.SubagentTally
		ok    bool
		want  *appwire.SubagentTally
	}{
		{"a tree with subagents", appwire.SubagentTally{Running: 2, Failed: 1, Done: 5}, true, &appwire.SubagentTally{Running: 2, Failed: 1, Done: 5}},
		{"a tree with none", appwire.SubagentTally{}, true, nil},
		{"no delegate tree", appwire.SubagentTally{Running: 1}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_root")
			srv.RecordDescendantAppEvent("th_root", events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_child", Data: events.UserInputData{Text: "go"}})
			srv.SetSubagentTallyFunc(func() (appwire.SubagentTally, bool) { return tc.tally, tc.ok })
			list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{IncludeSubagents: true, StatusOnly: true})
			if err != nil {
				t.Fatalf("thread/list: %v", err)
			}
			if len(list.Data) != 2 {
				t.Fatalf("rows = %+v, want the root and its child", list.Data)
			}
			if got := list.Data[0].Evener.Subagents; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("root tally = %+v, want %+v", got, tc.want)
			}
			if child := list.Data[1].Evener.Subagents; child != nil {
				t.Fatalf("the child row carries a tally: %+v", child)
			}
			if read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{}); read.Thread.Evener.Subagents != nil {
				t.Fatalf("thread/read carries a tally %+v; nothing announces its changes to a subscriber", read.Thread.Evener.Subagents)
			}
		})
	}
}
