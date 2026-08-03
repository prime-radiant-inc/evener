package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
)

func pinSectionAPIWeb(t *testing.T, metas ...schema.SessionMeta) (*WebServer, *hubcore.PinSectionStore) {
	t.Helper()
	past := hubcore.NewPastIndex("")
	past.SeedForTest(metas)
	store := hubcore.NewPinSectionStore(t.TempDir() + "/index.db")
	return NewWebServer(hubcore.WebConfig{Past: past, PinSections: store}), store
}

func topLevelMeta(id string) schema.SessionMeta {
	return schema.SessionMeta{ID: id, UpdatedAt: timeNowForTest()}
}

func apiRequest(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func postJSON(t *testing.T, handler http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return apiRequest(t, handler, http.MethodPost, target, body)
}

func patchJSON(t *testing.T, handler http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return apiRequest(t, handler, http.MethodPatch, target, body)
}

func getJSON(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	return apiRequest(t, handler, http.MethodGet, target, "")
}

func deleteURL(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	return apiRequest(t, handler, http.MethodDelete, target, "")
}

func decodeJSON[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var got T
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %d response %q: %v", rr.Code, rr.Body.String(), err)
	}
	return got
}

func assertJSONContains(t *testing.T, raw []byte, wants ...string) {
	t.Helper()
	compact := string(raw)
	for _, want := range wants {
		if !strings.Contains(compact, want) {
			t.Errorf("response %s does not contain %s", compact, want)
		}
	}
}

func TestAPISessionPinCreateReuseMoveAndUnpin(t *testing.T) {
	web, store := pinSectionAPIWeb(t, topLevelMeta("session-a"), topLevelMeta("session-b"))

	created := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-a","section_name":"Research"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	createdBody := decodeJSON[hubapi.SessionPinMutationResponse](t, created)
	if !createdBody.OK || !createdBody.Changed || createdBody.Assignment.SessionRef != "local:session-a" || createdBody.Assignment.Section.Name != "Research" || createdBody.Assignment.Section.MemberCount != 1 {
		t.Fatalf("create response = %+v", createdBody)
	}

	reused := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-b","section_name":" research "}`)
	if reused.Code != http.StatusOK {
		t.Fatalf("reuse = %d: %s", reused.Code, reused.Body.String())
	}
	reusedBody := decodeJSON[hubapi.SessionPinMutationResponse](t, reused)
	if reusedBody.Assignment.Section.ID != createdBody.Assignment.Section.ID || reusedBody.Assignment.Section.Name != "Research" || reusedBody.Assignment.Section.MemberCount != 2 {
		t.Fatalf("reuse response = %+v; created = %+v", reusedBody, createdBody)
	}

	moved := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-a","section_name":"Writing"}`)
	if moved.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", moved.Code, moved.Body.String())
	}
	movedBody := decodeJSON[hubapi.SessionPinMutationResponse](t, moved)
	if !movedBody.Changed || movedBody.Assignment.Section.Name != "Writing" || movedBody.Assignment.Section.MemberCount != 1 {
		t.Fatalf("move response = %+v", movedBody)
	}

	sections := getJSON(t, web.Handler(), "/api/pin-sections")
	if sections.Code != http.StatusOK {
		t.Fatalf("sections = %d: %s", sections.Code, sections.Body.String())
	}
	assertJSONContains(t, sections.Body.Bytes(), `"name":"Research"`, `"member_count":1`, `"name":"Writing"`)

	unpinned := deleteURL(t, web.Handler(), "/api/session-pin?ref=local%3Asession-a")
	if unpinned.Code != http.StatusOK {
		t.Fatalf("unpin = %d: %s", unpinned.Code, unpinned.Body.String())
	}
	unpinnedBody := decodeJSON[hubapi.SessionPinMutationResponse](t, unpinned)
	if !unpinnedBody.OK || !unpinnedBody.Changed || unpinnedBody.Assignment.SessionRef != "local:session-a" {
		t.Fatalf("unpin response = %+v", unpinnedBody)
	}

	got, err := store.Sections()
	if err != nil || len(got) != 2 || got[1].Name != "Writing" || got[1].MemberCount != 0 {
		t.Fatalf("sections = %+v, %v", got, err)
	}
}

func TestAPISessionPinPostValidation(t *testing.T) {
	overlong := strings.Repeat("x", hubcore.PinSectionNameMaxRunes+1)
	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "neither selector", body: `{"session_ref":"local:session-a"}`, code: http.StatusBadRequest},
		{name: "both selectors", body: `{"session_ref":"local:session-a","section_id":"missing","section_name":"Research"}`, code: http.StatusBadRequest},
		{name: "empty name", body: `{"session_ref":"local:session-a","section_name":" "}`, code: http.StatusBadRequest},
		{name: "overlong name", body: `{"session_ref":"local:session-a","section_name":"` + overlong + `"}`, code: http.StatusBadRequest},
		{name: "unknown section", body: `{"session_ref":"local:session-a","section_id":"missing"}`, code: http.StatusNotFound},
		{name: "cluster ref", body: `{"session_ref":"cluster:deadbeef","section_name":"Research"}`, code: http.StatusBadRequest},
		{name: "subagent ref", body: `{"session_ref":"local:subagent","section_name":"Research"}`, code: http.StatusBadRequest},
		{name: "fork ref", body: `{"session_ref":"local:fork","section_name":"Research"}`, code: http.StatusBadRequest},
		{name: "malformed ref", body: `{"session_ref":"local:","section_name":"Research"}`, code: http.StatusBadRequest},
		{name: "unknown ref", body: `{"session_ref":"local:unknown","section_name":"Research"}`, code: http.StatusBadRequest},
	}

	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		topLevelMeta("session-a"),
		{ID: "subagent", ParentSessionID: "session-a", IsSubagent: true, UpdatedAt: timeNowForTest()},
		{ID: "fork", ParentSessionID: "session-a", ForkLabel: "before edit", UpdatedAt: timeNowForTest()},
		{ID: "active-fork", ParentSessionID: "fork", UpdatedAt: timeNowForTest()},
	})
	store := hubcore.NewPinSectionStore(t.TempDir() + "/index.db")
	web := NewWebServer(hubcore.WebConfig{Past: past, PinSections: store})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := postJSON(t, web.Handler(), "/api/session-pin", tt.body)
			if rr.Code != tt.code {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.code, rr.Body.String())
			}
		})
	}
}

func TestAPIPinSectionRenameAndConflicts(t *testing.T) {
	web, store := pinSectionAPIWeb(t, topLevelMeta("session-a"), topLevelMeta("session-b"))
	now := timeNowForTest()
	one, _, err := store.CreateOrReuseAndAssign("Research", "session-a", now)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := store.CreateOrReuseAndAssign("Writing", "session-b", now)
	if err != nil {
		t.Fatal(err)
	}

	rr := patchJSON(t, web.Handler(), "/api/pin-sections/"+url.PathEscape(one.ID), `{"name":"RESEARCH"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("case rename = %d: %s", rr.Code, rr.Body.String())
	}
	assertJSONContains(t, rr.Body.Bytes(), `"changed":true`, `"name":"RESEARCH"`, `"member_count":1`)

	rr = patchJSON(t, web.Handler(), "/api/pin-sections/"+url.PathEscape(one.ID), `{"name":"Writing"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("conflict = %d: %s", rr.Code, rr.Body.String())
	}
	rr = patchJSON(t, web.Handler(), "/api/pin-sections/missing", `{"name":"Other"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing = %d: %s", rr.Code, rr.Body.String())
	}
	rr = patchJSON(t, web.Handler(), "/api/pin-sections/"+url.PathEscape(two.ID), `{"name":" "}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAPIPinSectionDeleteReturnsMemberCountAndRemovesAssignments(t *testing.T) {
	web, store := pinSectionAPIWeb(t, topLevelMeta("session-a"), topLevelMeta("session-b"))
	section, _, err := store.CreateOrReuseAndAssign("Research", "session-a", timeNowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Assign(section.ID, "session-b", timeNowForTest()); err != nil {
		t.Fatal(err)
	}

	rr := deleteURL(t, web.Handler(), "/api/pin-sections/"+url.PathEscape(section.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	assertJSONContains(t, rr.Body.Bytes(), `"ok":true`, `"changed":true`, `"member_count":2`)
	assignments, err := store.Assignments()
	if err != nil || len(assignments) != 0 {
		t.Fatalf("assignments = %+v, %v", assignments, err)
	}

	rr = deleteURL(t, web.Handler(), "/api/pin-sections/"+url.PathEscape(section.ID))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing delete = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAPISessionPinMutationsCallNotifyMutationOnlyOnChange(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{topLevelMeta("session-a")})
	store := hubcore.NewPinSectionStore(t.TempDir() + "/index.db")
	notifications := 0
	web := NewWebServer(hubcore.WebConfig{
		Past: past, PinSections: store,
		// notifyMutation performs the attention poke and tree broadcast together;
		// counting this synchronous seam avoids a network listener in the route test.
		PokeAttention: func() { notifications++ },
	})

	do := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		return apiRequest(t, web.Handler(), method, target, body)
	}
	created := do(http.MethodPost, "/api/session-pin", `{"session_ref":"local:session-a","section_name":"Research"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	if notifications != 1 {
		t.Fatalf("create notifications = %d, want 1", notifications)
	}

	noop := do(http.MethodPost, "/api/session-pin", `{"session_ref":"local:session-a","section_name":"research"}`)
	if noop.Code != http.StatusOK || !strings.Contains(noop.Body.String(), `"changed":false`) {
		t.Fatalf("no-op assign = %d: %s", noop.Code, noop.Body.String())
	}
	if notifications != 1 {
		t.Fatalf("no-op assign notifications = %d, want 1 total", notifications)
	}

	failed := do(http.MethodPost, "/api/session-pin", `{"session_ref":"local:session-a","section_id":"missing"}`)
	if failed.Code != http.StatusNotFound {
		t.Fatalf("failed assign = %d: %s", failed.Code, failed.Body.String())
	}
	if notifications != 1 {
		t.Fatalf("failed assign notifications = %d, want 1 total", notifications)
	}

	unpinned := do(http.MethodDelete, "/api/session-pin?ref=local%3Asession-a", "")
	if unpinned.Code != http.StatusOK || !strings.Contains(unpinned.Body.String(), `"changed":true`) {
		t.Fatalf("unpin = %d: %s", unpinned.Code, unpinned.Body.String())
	}
	if notifications != 2 {
		t.Fatalf("unpin notifications = %d, want 2 total", notifications)
	}

	noopUnpin := do(http.MethodDelete, "/api/session-pin?ref=local%3Asession-a", "")
	if noopUnpin.Code != http.StatusOK || !strings.Contains(noopUnpin.Body.String(), `"changed":false`) {
		t.Fatalf("no-op unpin = %d: %s", noopUnpin.Code, noopUnpin.Body.String())
	}
	if notifications != 2 {
		t.Fatalf("no-op unpin notifications = %d, want 2 total", notifications)
	}

	reassigned := do(http.MethodPost, "/api/session-pin", `{"session_ref":"local:session-a","section_name":"Research"}`)
	if reassigned.Code != http.StatusOK {
		t.Fatalf("reassign = %d: %s", reassigned.Code, reassigned.Body.String())
	}
	sectionID := decodeJSON[hubapi.SessionPinMutationResponse](t, reassigned).Assignment.Section.ID
	if notifications != 3 {
		t.Fatalf("reassign notifications = %d, want 3 total", notifications)
	}

	renamed := do(http.MethodPatch, "/api/pin-sections/"+url.PathEscape(sectionID), `{"name":"RESEARCH"}`)
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), `"changed":true`) {
		t.Fatalf("rename = %d: %s", renamed.Code, renamed.Body.String())
	}
	if notifications != 4 {
		t.Fatalf("rename notifications = %d, want 4 total", notifications)
	}

	noopRename := do(http.MethodPatch, "/api/pin-sections/"+url.PathEscape(sectionID), `{"name":"RESEARCH"}`)
	if noopRename.Code != http.StatusOK || !strings.Contains(noopRename.Body.String(), `"changed":false`) {
		t.Fatalf("no-op rename = %d: %s", noopRename.Code, noopRename.Body.String())
	}
	if notifications != 4 {
		t.Fatalf("no-op rename notifications = %d, want 4 total", notifications)
	}

	deleted := do(http.MethodDelete, "/api/pin-sections/"+url.PathEscape(sectionID), "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"changed":true`) {
		t.Fatalf("delete section = %d: %s", deleted.Code, deleted.Body.String())
	}
	if notifications != 5 {
		t.Fatalf("delete notifications = %d, want 5 total", notifications)
	}

	failedDelete := do(http.MethodDelete, "/api/pin-sections/"+url.PathEscape(sectionID), "")
	if failedDelete.Code != http.StatusNotFound {
		t.Fatalf("failed delete = %d: %s", failedDelete.Code, failedDelete.Body.String())
	}
	if notifications != 5 {
		t.Fatalf("failed delete notifications = %d, want 5 total", notifications)
	}
}

type failSecondMkdirFS struct {
	afero.Fs
	calls int
}

func (f *failSecondMkdirFS) MkdirAll(path string, perm os.FileMode) error {
	f.calls++
	if f.calls > 1 {
		return errors.New("unexpected post-commit store read")
	}
	return f.Fs.MkdirAll(path, perm)
}

func TestAPISessionPinSuccessDoesNotReadStoreAfterCommit(t *testing.T) {
	for _, tt := range []struct {
		name      string
		setup     func(t *testing.T, store *hubcore.PinSectionStore) string
		method    string
		body      string
		wantCount string
	}{
		{
			name: "create or reuse and assign",
			setup: func(t *testing.T, _ *hubcore.PinSectionStore) string {
				return "/api/session-pin"
			},
			method:    http.MethodPost,
			body:      `{"session_ref":"local:session-a","section_name":"Research"}`,
			wantCount: `"member_count":1`,
		},
		{
			name: "assign existing section",
			setup: func(t *testing.T, store *hubcore.PinSectionStore) string {
				section, _, err := store.CreateOrReuseAndAssign("Research", "session-seed", timeNowForTest())
				if err != nil {
					t.Fatal(err)
				}
				return "/api/session-pin?section=" + url.QueryEscape(section.ID)
			},
			method:    http.MethodPost,
			wantCount: `"member_count":2`,
		},
		{
			name: "rename",
			setup: func(t *testing.T, store *hubcore.PinSectionStore) string {
				section, _, err := store.CreateOrReuseAndAssign("Research", "session-a", timeNowForTest())
				if err != nil {
					t.Fatal(err)
				}
				return "/api/pin-sections/" + url.PathEscape(section.ID)
			},
			method:    http.MethodPatch,
			body:      `{"name":"RESEARCH"}`,
			wantCount: `"member_count":1`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{topLevelMeta("session-a")})
			store := hubcore.NewPinSectionStore(t.TempDir() + "/index.db")
			target := tt.setup(t, store)
			body := tt.body
			if tt.name == "assign existing section" {
				sectionID := strings.TrimPrefix(target, "/api/session-pin?section=")
				target = "/api/session-pin"
				body = `{"session_ref":"local:session-a","section_id":"` + sectionID + `"}`
			}
			fs := &failSecondMkdirFS{Fs: afero.NewOsFs()}
			store.SetFs(fs)
			notifications := 0
			web := NewWebServer(hubcore.WebConfig{
				Past: past, PinSections: store,
				PokeAttention: func() { notifications++ },
			})

			rr := apiRequest(t, web.Handler(), tt.method, target, body)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
			}
			assertJSONContains(t, rr.Body.Bytes(), `"changed":true`, tt.wantCount)
			if fs.calls != 1 || notifications != 1 {
				t.Fatalf("store opens = %d, notifications = %d; want 1, 1", fs.calls, notifications)
			}
		})
	}
}

func TestAPIPinSectionsNilStoreAndMethods(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	for _, tt := range []struct {
		method string
		target string
		body   string
		code   int
	}{
		{method: http.MethodGet, target: "/api/pin-sections", code: http.StatusInternalServerError},
		{method: http.MethodPost, target: "/api/session-pin", body: `{}`, code: http.StatusInternalServerError},
	} {
		rr := apiRequest(t, web.Handler(), tt.method, tt.target, tt.body)
		if rr.Code != tt.code {
			t.Errorf("%s %s = %d, want %d: %s", tt.method, tt.target, rr.Code, tt.code, rr.Body.String())
		}
	}

	configured, _ := pinSectionAPIWeb(t, topLevelMeta("session-a"))
	for _, tt := range []struct {
		method string
		target string
	}{
		{method: http.MethodPost, target: "/api/pin-sections"},
		{method: http.MethodGet, target: "/api/pin-sections/id"},
		{method: http.MethodPut, target: "/api/pin-sections/id"},
		{method: http.MethodGet, target: "/api/session-pin"},
		{method: http.MethodPatch, target: "/api/session-pin"},
	} {
		rr := apiRequest(t, configured.Handler(), tt.method, tt.target, `{}`)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405: %s", tt.method, tt.target, rr.Code, rr.Body.String())
		}
	}
}
