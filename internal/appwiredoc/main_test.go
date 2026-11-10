package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"primeradiant.com/evener/appwire"
)

func FuzzFieldsOf(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2} {
		f.Add(seed)
	}
	type sample struct {
		Visible string `json:"visible,omitempty"`
		Ignored string `json:"-"`
		Default int
	}
	f.Fuzz(func(t *testing.T, which uint8) {
		var typ reflect.Type
		switch which % 3 {
		case 0:
		case 1:
			typ = reflect.TypeFor[int]()
		default:
			typ = reflect.TypeFor[sample]()
		}
		fields := fieldsOf(typ)
		if typ != nil && typ.Kind() == reflect.Struct && len(fields) != 2 {
			t.Fatalf("fieldsOf returned %+v", fields)
		}
	})
}

func TestRun(t *testing.T) {
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	if got := run(nil, stderr, os.WriteFile); got != 2 {
		t.Fatalf("missing out code=%d", got)
	}
	if got := run([]string{"-bad"}, stderr, os.WriteFile); got != 2 {
		t.Fatalf("bad flag code=%d", got)
	}
	out := filepath.Join(t.TempDir(), "protocol.md")
	if got := run([]string{"-out", out}, stderr, os.WriteFile); got != 0 {
		t.Fatalf("success code=%d", got)
	}
	data, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(data), "AppWire") {
		t.Fatalf("output err=%v", err)
	}
	if got := run([]string{"-out", out}, stderr, func(string, []byte, os.FileMode) error { return errors.New("disk") }); got != 1 {
		t.Fatalf("write failure code=%d", got)
	}
}

func TestMainAndRenderFailure(t *testing.T) {
	oldExit, oldArgs, oldExecute := exitProcess, os.Args, executeTemplate
	oldStderr := os.Stderr
	mainStderr, err := os.CreateTemp(t.TempDir(), "main-stderr")
	if err != nil {
		t.Fatal(err)
	}
	renderStderr, err := os.CreateTemp(t.TempDir(), "render-stderr")
	if err != nil {
		_ = mainStderr.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		exitProcess, os.Args, executeTemplate, os.Stderr = oldExit, oldArgs, oldExecute, oldStderr
		_ = mainStderr.Close()
		_ = renderStderr.Close()
	})
	os.Args = []string{"appwiredoc"}
	os.Stderr = mainStderr
	var code int
	exitProcess = func(got int) { code = got }
	main()
	if code != 2 {
		t.Fatalf("main exit=%d", code)
	}
	if info, err := mainStderr.Stat(); err != nil {
		t.Fatalf("stat main stderr: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("main stderr was empty")
	}
	executeTemplate = func(*template.Template, *strings.Builder, docData) error { return errors.New("render") }
	if got := run([]string{"-out", "ignored"}, renderStderr, os.WriteFile); got != 1 {
		t.Fatalf("render code=%d", got)
	}
	if info, err := renderStderr.Stat(); err != nil {
		t.Fatalf("stat render stderr: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("render stderr was empty")
	}
}

func TestRegisterType(t *testing.T) {
	types := map[string]typeView{}
	if got := registerType(types, nil); got != "(inline)" {
		t.Fatalf("nil=%q", got)
	}
	value := struct{ Field string }{}
	if got := registerType(types, &value); !strings.HasPrefix(got, "struct {") {
		t.Fatalf("unnamed=%q", got)
	}
	_ = registerType(types, &value)
}

func TestBuild(t *testing.T) {
	d := build()
	if len(d.Methods) == 0 || len(d.Notifications) == 0 || len(d.Types) == 0 {
		t.Fatalf("incomplete document data: %+v", d)
	}
	for i := 1; i < len(d.Types); i++ {
		if d.Types[i-1].Name > d.Types[i].Name {
			t.Fatalf("types are not sorted: %q before %q", d.Types[i-1].Name, d.Types[i].Name)
		}
	}
}

func TestBuildIncludesJobActivityTypes(t *testing.T) {
	d := build()
	got := map[string]bool{}
	for _, tv := range d.Types {
		got[tv.Name] = true
	}
	for _, name := range []string{"JobActivityTree", "JobActivitySession", "JobActivityEntry", "JobActivityJob", "JobActivityDelegate", "JobActivityCounts", "JobActivityBranchState"} {
		if !got[name] {
			t.Fatalf("build() missing %s", name)
		}
	}
}

func TestBuildIncludesDelegateInfo(t *testing.T) {
	d := build()
	for _, tv := range d.Types {
		if tv.Name != "EvenerDelegateInfo" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"delegateId", "lifecycle", "phase", "status", "outcome", "transcriptRef"} {
			if !fields[name] {
				t.Fatalf("EvenerDelegateInfo missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing EvenerDelegateInfo")
}

// TestBuildIncludesSearchTopLevel guards the search grouping wire change:
// SearchTopLevel only ever nests inside SearchResult.TopLevel, never as a
// method's own Params/Result, so without an explicit registration it would
// get named in the field list but none of its fields documented - the same
// silent gap SearchResult itself needed registering for.
func TestBuildIncludesSearchTopLevel(t *testing.T) {
	d := build()
	for _, tv := range d.Types {
		if tv.Name != "SearchTopLevel" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"id", "ref", "title", "project", "state", "age", "askPending", "approvalPending"} {
			if !fields[name] {
				t.Fatalf("SearchTopLevel missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing SearchTopLevel")
}

// TestBuildIncludesInstanceEntry guards PR #758's review finding:
// InstanceEntry only ever appears nested inside InstanceListResponse's
// Instances field, never as a method's own Params/Result, so without an
// explicit registration it silently gets no field table at all - and a
// field added there (shadowedEnvVar) can be documented on its
// AuthStatusResponse twin while missing here with nothing to catch it.
func TestBuildIncludesInstanceEntry(t *testing.T) {
	d := build()
	for _, tv := range d.Types {
		if tv.Name != "InstanceEntry" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"name", "providerId", "activeSource", "envVar", "shadowedEnvVar", "credentialRequired"} {
			if !fields[name] {
				t.Fatalf("InstanceEntry missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing InstanceEntry")
}

// TestFieldsOfSkillInputContracts pins the skill input wire fields through
// the same reflection that renders the doc's field tables, so a tag rename
// or omitempty drift fails here rather than silently reshaping the
// generated protocol reference.
func TestFieldsOfSkillInputContracts(t *testing.T) {
	fieldViews := func(t *testing.T, typ reflect.Type) map[string]fieldView {
		t.Helper()
		fields := map[string]fieldView{}
		for _, field := range fieldsOf(typ) {
			fields[field.JSON] = field
		}
		return fields
	}

	capabilities := fieldViews(t, reflect.TypeFor[appwire.ThreadCapabilities]())
	skillInput, ok := capabilities["skillInput"]
	if !ok || skillInput.GoType != "bool" || !skillInput.Omitempty {
		t.Fatalf("ThreadCapabilities.skillInput = %+v, want an omitempty bool", skillInput)
	}

	skillInfo := fieldViews(t, reflect.TypeFor[appwire.EvenerSkillInfo]())
	for name, wantOmitempty := range map[string]bool{
		"name":                   false,
		"disableModelInvocation": false,
		"userInvocable":          false,
		"available":              false,
		"allowedTools":           true,
	} {
		field, ok := skillInfo[name]
		if !ok {
			t.Fatalf("EvenerSkillInfo missing field %q: %+v", name, skillInfo)
		}
		if field.Omitempty != wantOmitempty {
			t.Fatalf("EvenerSkillInfo.%s omitempty = %v, want %v", name, field.Omitempty, wantOmitempty)
		}
	}
	if field := skillInfo["allowedTools"]; field.GoType != "[]string" {
		t.Fatalf("EvenerSkillInfo.allowedTools Go type = %q, want []string", field.GoType)
	}

	diagnostic := fieldViews(t, reflect.TypeFor[appwire.EvenerSkillDiagnostic]())
	for name, wantOmitempty := range map[string]bool{
		"category":    false,
		"name":        true,
		"source":      false,
		"otherSource": true,
		"field":       true,
		"message":     false,
	} {
		field, ok := diagnostic[name]
		if !ok {
			t.Fatalf("EvenerSkillDiagnostic missing field %q: %+v", name, diagnostic)
		}
		if field.Omitempty != wantOmitempty {
			t.Fatalf("EvenerSkillDiagnostic.%s omitempty = %v, want %v", name, field.Omitempty, wantOmitempty)
		}
	}

	diagnostics := fieldViews(t, reflect.TypeFor[appwire.EvenerDiagnostics]())
	skillDiagnostics, ok := diagnostics["skillDiagnostics"]
	if !ok || skillDiagnostics.GoType != "[]appwire.EvenerSkillDiagnostic" || !skillDiagnostics.Omitempty {
		t.Fatalf("EvenerDiagnostics.skillDiagnostics = %+v, want an omitempty []appwire.EvenerSkillDiagnostic", skillDiagnostics)
	}
}

// TestBuildIncludesInstanceModelEntry guards the same gap one level
// down: InstanceModelEntry only ever appears nested inside
// InstanceEntry.models, so without an explicit registration it silently
// gets no field table of its own.
func TestBuildIncludesInstanceModelEntry(t *testing.T) {
	d := build()
	for _, tv := range d.Types {
		if tv.Name != "InstanceModelEntry" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"id", "disabled"} {
			if !fields[name] {
				t.Fatalf("InstanceModelEntry missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing InstanceModelEntry")
}

// TestBuildIncludesHostCredentialPushResult guards the same gap in the push
// report: HostCredentialPushResult is only ever the element type of
// HostPushCredentialsResponse.Results — never a method's own Params/Result — so
// without an explicit registration the protocol reference has no field table
// for the instance/action/reason a push reports, while
// appwire-client/typescript/types.gen.ts still emits the interface from the same
// catalog (roborev review of the 07c push).
func TestBuildIncludesHostCredentialPushResult(t *testing.T) {
	d := build()
	for _, tv := range d.Types {
		if tv.Name != "HostCredentialPushResult" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"instance", "action", "reason"} {
			if !fields[name] {
				t.Fatalf("HostCredentialPushResult missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing HostCredentialPushResult")
}

// TestBuildIncludesHubNotice guards the same gap RoboRev found on PR 22:
// HubNotice is only ever the element type of NoticesListResponse.Notices (a
// method result and a notification payload), never a method's own
// Params/Result, so without an explicit registration the reference names
// []appwire.HubNotice with no field table of its own while types.gen.ts
// still emits the interface.
func TestBuildIncludesHubNotice(t *testing.T) {
	d := build()
	for _, tv := range d.Types {
		if tv.Name != "HubNotice" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"id", "kind", "subject", "marketplace", "affectedSessions"} {
			if !fields[name] {
				t.Fatalf("HubNotice missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing HubNotice")
}

func TestBuildIncludesSessionActivityResources(t *testing.T) {
	want := map[string][]string{
		"SessionActivityAncestor": {"ref", "sessionId", "title"},
		"SessionActivityContext":  {"ref", "sessionId", "rootRef", "ancestors", "ancestryKnown", "epoch"},
		"SessionActivityCounts":   {"known", "total", "active", "failed", "completed"},
		"SessionActivityIssue":    {"ref", "code"},
		"SessionActivityPage":     {"nextCursor", "complete", "issues"},
		"SessionDelegate":         {"runGeneration", "reportPreview", "reportPreviewTruncated", "ownerRef", "childRef"},
		"SessionWatch":            {"ownerRef", "receiverRef", "state", "watch"},
	}
	got := make(map[string]typeView)
	for _, resource := range build().Types {
		got[resource.Name] = resource
	}
	for name, fields := range want {
		t.Run(name, func(t *testing.T) {
			resource, ok := got[name]
			if !ok {
				t.Fatalf("build() missing nested resource %s", name)
			}
			present := make(map[string]bool)
			for _, field := range resource.Fields {
				present[field.JSON] = true
			}
			for _, field := range fields {
				if !present[field] {
					t.Errorf("%s missing wire field %s", name, field)
				}
			}
		})
	}
}

// TestBuildIncludesActivityReadSessionActivity pins the pulse meter's element
// type. SessionActivity is not one of the evener/thread/* activity resources
// above: ActivityReadResponse.Sessions is its only catalog reference, and a
// consumer of evener/activity/read needs its field table.
func TestBuildIncludesActivityReadSessionActivity(t *testing.T) {
	want := []string{"ref", "minutes", "runningSubagents", "quietForMs", "latestIntent", "lastMovedAt"}
	for _, resource := range build().Types {
		if resource.Name != "SessionActivity" {
			continue
		}
		present := make(map[string]bool, len(resource.Fields))
		for _, field := range resource.Fields {
			present[field.JSON] = true
		}
		for _, field := range want {
			if !present[field] {
				t.Errorf("SessionActivity missing wire field %s", field)
			}
		}
		return
	}
	t.Fatal("build() missing SessionActivity")
}

func TestBuildIncludesJobOutputPage(t *testing.T) {
	for _, tv := range build().Types {
		if tv.Name != "JobOutputPage" {
			continue
		}
		fields := map[string]bool{}
		for _, field := range tv.Fields {
			fields[field.JSON] = true
		}
		for _, name := range []string{"offsetBytes", "bytesReturned", "totalBytes", "retainedStartBytes", "encoding", "data"} {
			if !fields[name] {
				t.Fatalf("JobOutputPage missing field %q: %+v", name, tv.Fields)
			}
		}
		return
	}
	t.Fatal("build() missing JobOutputPage")
}

func TestNestedSchemaVisibility(t *testing.T) {
	value := struct {
		Visible struct {
			Clients map[string][]*appwire.ClientInfo `json:"clients"`
		} `json:"visible"`
		Ignored appwire.Capabilities `json:"-"`
		hidden  appwire.Thread
	}{}
	types := map[string]typeView{}
	registerType(types, value)
	registerType(types, value) // Shared roots do not duplicate or re-enter schemas.
	if len(types) != 2 || len(types["ClientInfo"].Fields) != 2 {
		t.Fatalf("want only root and ClientInfo schemas, got %+v", types)
	}
}

func TestReachableWireSchemasAndLinks(t *testing.T) {
	d := build()
	types := map[string]typeView{}
	for _, typ := range d.Types {
		if _, exists := types[typ.Name]; exists {
			t.Fatalf("duplicate schema %s", typ.Name)
		}
		types[typ.Name] = typ
	}
	for _, name := range []string{"Thread", "InputItem", "MutationReceipt", "ClientInfo", "Capabilities", "ThreadContentParams"} {
		if len(types[name].Fields) == 0 {
			t.Errorf("missing reachable schema %s", name)
		}
	}
	var markdown []byte
	if code := run([]string{"-out", "unused"}, os.Stderr, func(_ string, data []byte, _ os.FileMode) error {
		markdown = data
		return nil
	}); code != 0 {
		t.Fatalf("run = %d", code)
	}
	// Check navigation structure, not explanatory copy.
	links := regexp.MustCompile(`\]\(#([^)]*)\)`).FindAllSubmatch(markdown, -1)
	linkTargets := map[string]bool{}
	for _, match := range links {
		linkTargets[string(match[1])] = true
	}
	for _, name := range []string{"InitializeParams", "InitializeResponse", "ClientInfo", "Thread", "ThreadContentParams", "InputItem"} {
		anchor := strings.ToLower(name)
		if !linkTargets[anchor] {
			t.Errorf("missing schema link #%s", anchor)
		}
	}
	// Start independently at every catalog root, not the generator's registered
	// types. A visited set makes recursive schemas safe and repeated edges cheap.
	var pending []reflect.Type
	for _, method := range appwire.Methods {
		pending = append(pending, reflect.TypeOf(method.Params))
		if arms, union := appwire.MethodResultArms[method.Name]; union {
			for _, arm := range arms {
				pending = append(pending, reflect.TypeOf(arm))
			}
		} else {
			pending = append(pending, reflect.TypeOf(method.Result))
		}
	}
	for _, notification := range appwire.Notifications {
		pending = append(pending, reflect.TypeOf(notification.Payload))
	}
	seen := map[reflect.Type]bool{}
	for len(pending) > 0 {
		typ := pending[0]
		pending = pending[1:]
		if typ == nil || seen[typ] {
			continue
		}
		seen[typ] = true
		if typ.PkgPath() != "" && typ.PkgPath() != "primeradiant.com/evener/appwire" {
			continue
		}
		if typ.Name() != "" && typ.PkgPath() != "" {
			if _, ok := types[typ.Name()]; !ok {
				t.Errorf("reachable catalog type %s has no schema", typ)
			}
			if !linkTargets[strings.ToLower(typ.Name())] {
				t.Errorf("reachable catalog type %s has no incoming link", typ)
			}
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Array, reflect.Slice:
			pending = append(pending, typ.Elem())
		case reflect.Map:
			pending = append(pending, typ.Key(), typ.Elem())
		case reflect.Struct:
			for field := range typ.Fields() {
				if field.PkgPath == "" && field.Tag.Get("json") != "-" {
					pending = append(pending, field.Type)
				}
			}
		}
	}
	for _, name := range []string{"Time", "Client", "Message"} {
		if _, ok := types[name]; ok {
			t.Errorf("implementation type %s leaked into schemas", name)
		}
	}
	anchors := map[string]bool{}
	for _, match := range regexp.MustCompile("(?m)^### `(\\w+)`$").FindAllSubmatch(markdown, -1) {
		anchors[strings.ToLower(string(match[1]))] = true
	}
	for _, match := range links {
		if !anchors[string(match[1])] {
			t.Errorf("unresolved schema link %s", match[1])
		}
	}
}
