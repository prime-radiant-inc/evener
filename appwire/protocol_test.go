package appwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// TestMethodCatalogWellFormed guards the catalog's internal invariants: the
// generated protocol doc and the router cross-checks both rely on these.
func TestMethodCatalogWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, m := range Methods {
		if m.Name == "" {
			t.Errorf("Methods[%d] has empty Name", i)
		}
		if seen[m.Name] {
			t.Errorf("Methods has duplicate entry for %q", m.Name)
		}
		seen[m.Name] = true
		if m.Params == nil {
			t.Errorf("method %q has nil Params", m.Name)
		}
		if m.Result == nil {
			t.Errorf("method %q has nil Result", m.Name)
		}
		switch m.Scope {
		case ScopeBoth, ScopeHub, ScopeDaemon, ScopeConnection, ScopeUnimplemented:
		default:
			t.Errorf("method %q has invalid scope %q", m.Name, m.Scope)
		}
		if m.Summary == "" {
			t.Errorf("method %q has empty Summary", m.Name)
		}
	}
}

func TestNotificationCatalogWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, n := range Notifications {
		if n.Name == "" {
			t.Errorf("Notifications[%d] has empty Name", i)
		}
		if seen[n.Name] {
			t.Errorf("Notifications has duplicate entry for %q", n.Name)
		}
		seen[n.Name] = true
		if n.Summary == "" {
			t.Errorf("notification %q has empty Summary", n.Name)
		}
	}
	for name, want := range map[string]any{
		NotifySerfJobsTreeUpdated: JobsTreeUpdatedParams{},
	} {
		for _, n := range Notifications {
			if n.Name != name {
				continue
			}
			if reflect.TypeOf(n.Payload) != reflect.TypeOf(want) {
				t.Fatalf("notification %q payload type = %T, want %T", name, n.Payload, want)
			}
			goto found
		}
		t.Fatalf("notification %q missing from catalog", name)
	found:
	}
}

func TestJobsListReplacementTreeWireShape(t *testing.T) {
	payload := JobActivityTree{
		Revision: 7,
		Root: JobActivitySession{
			SessionID: "root", Ref: "local:root", Label: "Root",
			Counts: JobActivityCounts{Active: 2, Failed: 0, Completed: 1, Complete: true},
			Entries: []JobActivityEntry{
				{Kind: "shell", Job: &JobActivityJob{JobID: "job_shell", OwnerSessionID: "root", OwnerRef: "local:root", Type: "shell", Status: "running", Terminal: false}},
				{Kind: "delegate", Delegate: &JobActivityDelegate{DelegateID: "dlg_1", ChildSessionID: "child", ChildRef: "local:child"}},
			},
		},
	}
	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"revision":7`, `"kind":"shell"`, `"delegateId":"dlg_1"`, `"ownerRef":"local:root"`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("wire %s missing %s", got, want)
		}
	}
}

// TestConnectionAndReservedMethodsCataloged pins the special cases the router
// cross-checks deliberately skip, so a future change can't silently drop them.
func TestConnectionAndReservedMethodsCataloged(t *testing.T) {
	scope := map[string]MethodScope{}
	for _, m := range Methods {
		scope[m.Name] = m.Scope
	}
	for name, want := range map[string]MethodScope{
		MethodInitialize:          ScopeConnection,
		MethodPing:                ScopeConnection,
		MethodThreadTurnItemsList: ScopeUnimplemented,
	} {
		if got, ok := scope[name]; !ok {
			t.Errorf("method %q missing from catalog", name)
		} else if got != want {
			t.Errorf("method %q scope = %q, want %q", name, got, want)
		}
	}
}

func TestMutationShapesRequireIdentityAndPreconditions(t *testing.T) {
	tests := []struct {
		method string
		valid  string
	}{
		{MethodTurnStart, `{"clientMutationId":"m1"}`},
		{MethodTurnSteer, `{"clientMutationId":"m1","expectedTurnId":"t1"}`},
		{MethodTurnInterrupt, `{"clientMutationId":"m1","expectedTurnId":"t1"}`},
		{MethodTurnQueue, `{"clientMutationId":"m1","expectedTurnId":"t1"}`},
		{MethodTurnDrainAsSteer, `{"clientMutationId":"m1","expectedTurnId":"t1","expectedQueueRevision":0}`},
		{MethodTurnPromoteQueuedAsSteer, `{"clientMutationId":"m1","expectedTurnId":"t1","expectedEntryId":"q1"}`},
		{MethodTurnCancelQueued, `{"clientMutationId":"m1","expectedEntryId":"q1"}`},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			if err := ValidateMutationParams(tc.method, json.RawMessage(tc.valid)); err != nil {
				t.Fatalf("valid shape rejected: %v", err)
			}
			if err := ValidateMutationParams(tc.method, json.RawMessage(`{}`)); err == nil {
				t.Fatal("empty mutation shape accepted")
			}
		})
	}
}

func TestMutationExpectedQueueRevisionRequiresUnsignedInteger(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "zero", value: "0"},
		{name: "null", value: "null", wantErr: true},
		{name: "fractional", value: "1.5", wantErr: true},
		{name: "negative", value: "-1", wantErr: true},
		{name: "string", value: `"0"`, wantErr: true},
		{name: "overflow", value: "18446744073709551616", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"clientMutationId":"m1","expectedTurnId":"t1","expectedQueueRevision":` + tc.value + `}`)
			err := ValidateMutationParams(MethodTurnDrainAsSteer, raw)
			if tc.wantErr && err == nil {
				t.Fatal("invalid expectedQueueRevision accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("valid expectedQueueRevision rejected: %v", err)
			}
		})
	}
}

func TestThreadNotificationsRequireAuthoritativeRoutingIdentity(t *testing.T) {
	global := map[string]bool{
		NotifySerfAuthUpdated:        true,
		NotifySerfLaunchUpdated:      true,
		NotifySerfAttentionChanged:   true,
		NotifySerfMarketplaceUpdated: true,
		NotifySerfPluginUpdated:      true,
		NotifySerfTreeChanged:        true,
	}
	for _, notification := range Notifications {
		if global[notification.Name] {
			continue
		}
		payloadType := reflect.TypeOf(notification.Payload)
		for _, fieldName := range []string{"ThreadID", "Ref"} {
			field, ok := payloadType.FieldByName(fieldName)
			if !ok {
				t.Errorf("%s payload %s has no %s", notification.Name, payloadType.Name(), fieldName)
				continue
			}
			if got, want := field.Tag.Get("json"), map[string]string{"ThreadID": "threadId", "Ref": "ref"}[fieldName]; got != want {
				t.Errorf("%s payload %s.%s json tag = %q, want %q", notification.Name, payloadType.Name(), fieldName, got, want)
			}
		}
	}
}

// TestJobsCatalogEntries pins the jobs-panel wire entries so the protocol
// catalog can't drift from the method constants.
func TestJobsCatalogEntries(t *testing.T) {
	methods := map[string]bool{}
	for _, e := range Methods {
		methods[e.Name] = true
	}
	for _, m := range []string{MethodSerfJobsList, MethodSerfJobsOutput} {
		if !methods[m] {
			t.Errorf("request catalog missing %s", m)
		}
	}
	// The panel's live refetch trigger is serf/job/started|finished, not a
	// notification of its own (kata j7y6), so those two are all this pins.
	notifs := map[string]bool{}
	for _, e := range Notifications {
		notifs[e.Name] = true
	}
	for _, n := range []string{NotifySerfJobStarted, NotifySerfJobFinished} {
		if !notifs[n] {
			t.Errorf("notification catalog missing %s", n)
		}
	}
}
