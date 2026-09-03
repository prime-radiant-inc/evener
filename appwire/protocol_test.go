package appwire

import (
	"bytes"
	"encoding/json"
	"os"
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

func TestPluginPreviewIsHubOnly(t *testing.T) {
	var found *MethodSpec
	for i := range Methods {
		if Methods[i].Name == MethodEvenerPluginPreview {
			found = &Methods[i]
			break
		}
	}
	if found == nil {
		t.Fatal("plugin preview missing from method catalog")
	}
	if found.Scope != ScopeHub {
		t.Fatalf("plugin preview scope = %q, want hub", found.Scope)
	}
	if _, ok := found.Params.(PluginPreviewParams); !ok {
		t.Fatalf("plugin preview params = %T", found.Params)
	}
	if _, ok := found.Result.(PluginPreviewResponse); !ok {
		t.Fatalf("plugin preview result = %T", found.Result)
	}
	for _, name := range CatalogMethodNames(ScopeDaemon) {
		if name == MethodEvenerPluginPreview {
			t.Fatal("plugin preview must not be in daemon catalog")
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
		NotifyEvenerDelegateUpdated:       EvenerDelegateParams{},
		NotifyEvenerJobsTreeUpdated:       JobsTreeUpdatedParams{},
		NotifyEvenerNavigationInvalidated: NavigationInvalidatedPayload{},
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

func TestNavigationInvalidatedPayloadJSON(t *testing.T) {
	payload := NavigationInvalidatedPayload{
		GenerationID: "generation-a",
		Sequence:     7,
		Targets: []NavigationInvalidationTarget{{
			Kind:       NavigationTargetProject,
			ProjectKey: "project-key",
			Revision:   3,
		}},
	}
	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"generationId":"generation-a","sequence":7,"targets":[{"kind":"project","projectKey":"project-key","revision":3}]}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestNavigationInvalidationTargetVariants(t *testing.T) {
	tests := []struct {
		name   string
		target NavigationInvalidationTarget
		want   string
	}{
		{"manifest", NavigationInvalidationTarget{Kind: NavigationTargetManifest, Revision: 1}, `{"kind":"manifest","revision":1}`},
		{"section", NavigationInvalidationTarget{Kind: NavigationTargetSection, Section: "live", Revision: 2}, `{"kind":"section","section":"live","revision":2}`},
		{"pin catalog", NavigationInvalidationTarget{Kind: NavigationTargetPinCatalog, Revision: 3}, `{"kind":"pin_catalog","revision":3}`},
		{"pin section", NavigationInvalidationTarget{Kind: NavigationTargetPinSection, SectionID: "pin-a", Revision: 4}, `{"kind":"pin_section","sectionId":"pin-a","revision":4}`},
		{"catalog", NavigationInvalidationTarget{Kind: NavigationTargetCatalog, Catalog: "projects", Revision: 5}, `{"kind":"catalog","catalog":"projects","revision":5}`},
		{"all loaded projects", NavigationInvalidationTarget{Kind: NavigationTargetAllLoadedProjects}, `{"kind":"all_loaded_projects"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNavigationInvalidationTargetRejectsInvalidVariants(t *testing.T) {
	valid := []NavigationInvalidationTarget{
		{Kind: NavigationTargetManifest, Revision: 0},
		{Kind: NavigationTargetSection, Section: "live", Revision: 1},
		{Kind: NavigationTargetPinCatalog, Revision: 2},
		{Kind: NavigationTargetPinSection, SectionID: "pin-a", Revision: 3},
		{Kind: NavigationTargetCatalog, Catalog: "projects", Revision: 4},
		{Kind: NavigationTargetProject, ProjectKey: "project-a", Revision: 5},
		{Kind: NavigationTargetAllLoadedProjects},
	}
	for _, target := range valid {
		if _, err := json.Marshal(target); err != nil {
			t.Errorf("marshal valid %+v: %v", target, err)
		}
	}

	invalid := []NavigationInvalidationTarget{
		{Kind: "unknown", Revision: 1},
		{Kind: NavigationTargetManifest, Section: "live", Revision: 1},
		{Kind: NavigationTargetSection, Revision: 1},
		{Kind: NavigationTargetPinCatalog, Catalog: "projects", Revision: 1},
		{Kind: NavigationTargetPinSection, Revision: 1},
		{Kind: NavigationTargetCatalog, Revision: 1},
		{Kind: NavigationTargetProject, Revision: 1},
		{Kind: NavigationTargetAllLoadedProjects, Revision: 1},
		{Kind: NavigationTargetAllLoadedProjects, ProjectKey: "project-a"},
	}
	for _, target := range invalid {
		if _, err := json.Marshal(target); err == nil {
			t.Errorf("marshal invalid %+v succeeded", target)
		}
	}
}

func TestNavigationInvalidationTargetUnmarshalRejectsInvalidVariants(t *testing.T) {
	valid := []string{
		`{"kind":"manifest","revision":0}`,
		`{"kind":"section","section":"live","revision":1}`,
		`{"kind":"pin_catalog","revision":2}`,
		`{"kind":"pin_section","sectionId":"pin-a","revision":3}`,
		`{"kind":"catalog","catalog":"projects","revision":4}`,
		`{"kind":"project","projectKey":"project-a","revision":5}`,
		`{"kind":"all_loaded_projects"}`,
	}
	for _, raw := range valid {
		var target NavigationInvalidationTarget
		if err := json.Unmarshal([]byte(raw), &target); err != nil {
			t.Errorf("unmarshal valid %s: %v", raw, err)
		}
	}

	invalid := []string{
		`{"kind":"unknown","revision":1}`,
		`{"kind":"manifest"}`,
		`{"kind":"manifest","revision":1,"section":"live"}`,
		`{"kind":"section","revision":1}`,
		`{"kind":"pin_catalog","revision":1,"catalog":"projects"}`,
		`{"kind":"pin_section","revision":1}`,
		`{"kind":"catalog","revision":1}`,
		`{"kind":"project","revision":1}`,
		`{"kind":"all_loaded_projects","revision":1}`,
		`{"kind":"manifest","revision":1,"unexpected":true}`,
	}
	for _, raw := range invalid {
		var target NavigationInvalidationTarget
		if err := json.Unmarshal([]byte(raw), &target); err == nil {
			t.Errorf("unmarshal invalid %s succeeded", raw)
		}
	}
}

func TestTranscriptDisplayCatalog(t *testing.T) {
	methods := map[string]MethodSpec{}
	for _, method := range Methods {
		methods[method.Name] = method
	}
	for _, name := range []string{
		MethodEvenerSettingsTranscriptDisplayGet,
		MethodEvenerSettingsTranscriptDisplayPatch,
	} {
		method, ok := methods[name]
		if !ok {
			t.Fatalf("method catalog missing %s", name)
		}
		if method.Scope != ScopeHub {
			t.Errorf("method %s scope = %q, want %q", name, method.Scope, ScopeHub)
		}
	}
	var changed *NotificationSpec
	for i := range Notifications {
		if Notifications[i].Name == NotifyEvenerSettingsTranscriptDisplayChanged {
			changed = &Notifications[i]
			break
		}
	}
	if changed == nil {
		t.Fatalf("notification catalog missing %s", NotifyEvenerSettingsTranscriptDisplayChanged)
	}
	if reflect.TypeOf(changed.Payload) != reflect.TypeFor[TranscriptDisplayChangedParams]() {
		t.Fatalf("changed payload type = %T, want %T", changed.Payload, TranscriptDisplayChangedParams{})
	}
}

func TestKeybindingsCatalog(t *testing.T) {
	methods := map[string]MethodSpec{}
	for _, method := range Methods {
		methods[method.Name] = method
	}
	for _, name := range []string{
		MethodEvenerSettingsKeybindingsGet,
		MethodEvenerSettingsKeybindingsPatch,
	} {
		method, ok := methods[name]
		if !ok {
			t.Fatalf("method catalog missing %s", name)
		}
		if method.Scope != ScopeHub {
			t.Errorf("method %s scope = %q, want %q", name, method.Scope, ScopeHub)
		}
	}
	var changed *NotificationSpec
	for i := range Notifications {
		if Notifications[i].Name == NotifyEvenerSettingsKeybindingsChanged {
			changed = &Notifications[i]
			break
		}
	}
	if changed == nil {
		t.Fatalf("notification catalog missing %s", NotifyEvenerSettingsKeybindingsChanged)
	}
	if reflect.TypeOf(changed.Payload) != reflect.TypeFor[KeybindingsOverrides]() {
		t.Fatalf("changed payload type = %T, want %T", changed.Payload, KeybindingsOverrides{})
	}
}

func TestFeatureSetKeybindingsJSONField(t *testing.T) {
	encoded, err := json.Marshal(FeatureSet{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"keybindingsSettings"`)) {
		t.Fatalf("false feature must be omitted: %s", encoded)
	}
	encoded, err = json.Marshal(FeatureSet{KeybindingsSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"keybindingsSettings":true`)) {
		t.Fatalf("true feature missing from JSON: %s", encoded)
	}

	generated, err := os.ReadFile("../cmd/evener-hub/frontend/src/protocol/types.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(generated, []byte("keybindingsSettings?: boolean;")) {
		t.Fatal("generated TypeScript feature field is not optional")
	}
}

func TestFeatureSetTranscriptDisplayJSONField(t *testing.T) {
	encoded, err := json.Marshal(FeatureSet{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"transcriptDisplaySettings"`)) {
		t.Fatalf("false feature must be omitted: %s", encoded)
	}
	encoded, err = json.Marshal(FeatureSet{TranscriptDisplaySettings: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"transcriptDisplaySettings":true`)) {
		t.Fatalf("true feature missing from JSON: %s", encoded)
	}

	generated, err := os.ReadFile("../cmd/evener-hub/frontend/src/protocol/types.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(generated, []byte("transcriptDisplaySettings?: boolean;")) {
		t.Fatal("generated TypeScript feature field is not optional")
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

func TestNavigationReadMethodIsHubScoped(t *testing.T) {
	for _, method := range Methods {
		if method.Name != MethodEvenerNavigationRead {
			continue
		}
		if method.Scope != ScopeHub {
			t.Fatalf("navigation read scope = %q, want %q", method.Scope, ScopeHub)
		}
		if _, ok := method.Params.(NavigationReadParams); !ok {
			t.Fatalf("navigation read params type = %T, want NavigationReadParams", method.Params)
		}
		if _, ok := method.Result.(NavigationReadResponse); !ok {
			t.Fatalf("navigation read result type = %T, want NavigationReadResponse", method.Result)
		}
		if method.Summary == "" {
			t.Fatal("navigation read catalog summary is empty")
		}
		return
	}
	t.Fatalf("method catalog missing %q", MethodEvenerNavigationRead)
}

func TestMutationShapesRequireIdentityAndPreconditions(t *testing.T) {
	tests := []struct {
		method string
		valid  string
	}{
		{MethodTurnStart, `{"clientMutationId":"m1","expectedInstanceId":"i1"}`},
		{MethodTurnSteer, `{"clientMutationId":"m1","expectedInstanceId":"i1"}`},
		{MethodTurnInterrupt, `{"clientMutationId":"m1","expectedInstanceId":"i1"}`},
		{MethodTurnQueue, `{"clientMutationId":"m1","expectedInstanceId":"i1"}`},
		{MethodTurnDrainAsSteer, `{"clientMutationId":"m1","expectedInstanceId":"i1","expectedQueueRevision":0}`},
		{MethodTurnPromoteQueuedAsSteer, `{"clientMutationId":"m1","expectedInstanceId":"i1","expectedEntryId":"q1"}`},
		{MethodTurnCancelQueued, `{"clientMutationId":"m1","expectedInstanceId":"i1","expectedEntryId":"q1"}`},
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
			raw := json.RawMessage(`{"clientMutationId":"m1","expectedInstanceId":"i1","expectedQueueRevision":` + tc.value + `}`)
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
		NotifyEvenerAuthUpdated:                      true,
		NotifyEvenerLaunchUpdated:                    true,
		NotifyEvenerAttentionChanged:                 true,
		NotifyEvenerMarketplaceUpdated:               true,
		NotifyEvenerPluginUpdated:                    true,
		NotifyEvenerNavigationInvalidated:            true,
		NotifyEvenerSettingsTranscriptDisplayChanged: true,
		NotifyEvenerSettingsKeybindingsChanged:       true,
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
		// The daemon restamps threadId/ref at its notification egress
		// (stampAppNotificationTarget); a payload without the method would
		// silently ship the projector's own view of the target instead.
		if _, ok := notification.Payload.(NotificationTargeted); !ok {
			t.Errorf("%s payload %s does not implement NotificationTargeted", notification.Name, payloadType.Name())
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
	for _, m := range []string{MethodEvenerJobsList, MethodEvenerJobsOutput} {
		if !methods[m] {
			t.Errorf("request catalog missing %s", m)
		}
	}
	// The panel's live refetch trigger is evener/job/started|finished, not a
	// notification of its own (kata j7y6), so those two are all this pins.
	notifs := map[string]bool{}
	for _, e := range Notifications {
		notifs[e.Name] = true
	}
	for _, n := range []string{NotifyEvenerJobStarted, NotifyEvenerJobFinished} {
		if !notifs[n] {
			t.Errorf("notification catalog missing %s", n)
		}
	}
}

// TestControlMutationsRequireNoTurnID pins the flag-day validator's half of the
// session-scoped rule: control applies to whatever is running, so no control
// mutation may demand a turn id -- while the stable workspace identity and
// current instance fence every retry-safe mutation.
func TestControlMutationsRequireNoTurnID(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		params  string
		wantErr bool
	}{
		{
			name:   "interrupt needs no expected turn",
			method: MethodTurnInterrupt,
			params: `{"clientMutationId":"m1","expectedInstanceId":"i1"}`,
		},
		{
			name:   "steer needs no expected turn",
			method: MethodTurnSteer,
			params: `{"clientMutationId":"m1","expectedInstanceId":"i1"}`,
		},
		{
			name:   "queue needs no expected turn",
			method: MethodTurnQueue,
			params: `{"clientMutationId":"m1","expectedInstanceId":"i1"}`,
		},
		{
			name:    "interrupt still needs an identity",
			method:  MethodTurnInterrupt,
			params:  `{}`,
			wantErr: true,
		},
		{
			name:   "drain needs the queue revision it swaps against, not a turn",
			method: MethodTurnDrainAsSteer,
			params: `{"clientMutationId":"m1","expectedInstanceId":"i1","expectedQueueRevision":3}`,
		},
		{
			name:    "drain without its queue revision is still refused",
			method:  MethodTurnDrainAsSteer,
			params:  `{"clientMutationId":"m1"}`,
			wantErr: true,
		},
		{
			name:   "promote needs the entry it moves, not a turn",
			method: MethodTurnPromoteQueuedAsSteer,
			params: `{"clientMutationId":"m1","expectedInstanceId":"i1","expectedEntryId":"qe_1"}`,
		},
		{
			name:    "promote without its entry is still refused",
			method:  MethodTurnPromoteQueuedAsSteer,
			params:  `{"clientMutationId":"m1"}`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMutationParams(tc.method, json.RawMessage(tc.params))
			if tc.wantErr && err == nil {
				t.Fatal("invalid mutation shape accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("valid mutation shape rejected: %v", err)
			}
		})
	}
}
