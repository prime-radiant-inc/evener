package apptranscript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestMergeThreadItemsAuthorshipPrecedence(t *testing.T) {
	t.Parallel()
	known := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:origin"}, Inherited: true}
	replacement := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:other"}}
	unknown := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorUnknown}}
	for _, tc := range []struct {
		name                     string
		existing, incoming, want *appwire.TranscriptAuthorship
	}{
		{name: "omitted preserves known", existing: known, want: known},
		{name: "explicit author wins", existing: known, incoming: replacement, want: replacement},
		{name: "explicit unknown wins", existing: known, incoming: unknown, want: unknown},
		{name: "omitted preserves unknown", existing: unknown, want: unknown},
		{name: "absent stays absent"},
		{name: "incoming supplies author", incoming: replacement, want: replacement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := appwire.ThreadItem{Type: "commandExecution", CallID: "call_tool", Authorship: tc.existing, Status: appwire.TurnStatusInProgress}
			incoming := appwire.ThreadItem{Type: "commandExecution", CallID: "call_tool", Authorship: tc.incoming, Status: appwire.TurnStatusCompleted}
			merged := MergeThreadItems(existing, incoming)
			if merged.Status != appwire.TurnStatusCompleted || !reflect.DeepEqual(merged.Authorship, tc.want) {
				t.Errorf("merged status=%s authorship=%+v, want completed with %+v", merged.Status, merged.Authorship, tc.want)
			}
		})
	}
}
