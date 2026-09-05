package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubNavigationReadRejectsV1Params(t *testing.T) {
	service := newNavigationReadTestService(t)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, service)
	for _, raw := range []string{
		`{"resource":"manifest"}`,
		`{"resource":"manifest","representationVersion":1}`,
		`{"resource":"manifest","representationVersion":2,"etag":"abc"}`,
		`{"resource":"manifest","representationVersion":2,"base":{"generationId":"g","revision":1,"etag":"e"},"etag":"abc"}`,
	} {
		_, err := dispatchNavigationReadRaw(t, server, raw)
		assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	}
}

func TestHubNavigationReadServesEveryResourceFamily(t *testing.T) {
	service := newNavigationReadTestService(t)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, service)

	tests := []struct {
		name         string
		params       appwire.NavigationReadParams
		wantEntities int
	}{
		{
			name:   "manifest",
			params: appwire.NavigationReadParams{Resource: "manifest"},
		},
		{
			name:         "live section",
			params:       appwire.NavigationReadParams{Resource: "section", Section: "live"},
			wantEntities: 1,
		},
		{
			name:   "needs you section",
			params: appwire.NavigationReadParams{Resource: "section", Section: "needs_you"},
		},
		{
			name:         "pin catalog",
			params:       appwire.NavigationReadParams{Resource: "pin_catalog"},
			wantEntities: 1,
		},
		{
			name:         "pin section",
			params:       appwire.NavigationReadParams{Resource: "pin_section", SectionID: "pin-a"},
			wantEntities: 1,
		},
		{
			name:         "projects catalog",
			params:       appwire.NavigationReadParams{Resource: "catalog", Catalog: "projects"},
			wantEntities: 1,
		},
		{
			name:   "archived projects catalog",
			params: appwire.NavigationReadParams{Resource: "catalog", Catalog: "archived_projects"},
		},
		{
			name:   "test runs catalog",
			params: appwire.NavigationReadParams{Resource: "catalog", Catalog: "test_runs"},
		},
		{
			name:         "project",
			params:       appwire.NavigationReadParams{Resource: "project", ProjectKey: "p1"},
			wantEntities: 2,
		},
		{
			name:         "project page",
			params:       appwire.NavigationReadParams{Resource: "project_page", ProjectKey: "p1", Tier: "current"},
			wantEntities: 1,
		},
		{
			name:         "location",
			params:       appwire.NavigationReadParams{Resource: "location", Ref: navigationTestSessionID},
			wantEntities: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := dispatchNavigationRead(t, server, tt.params)
			if response.Status != "ok" {
				t.Fatalf("status = %q, want ok", response.Status)
			}
			if response.Representation != appwire.NavigationRepresentationSnapshot {
				t.Fatalf("representation = %q, want snapshot", response.Representation)
			}
			if response.GenerationID == "" || response.Revision == 0 || response.ETag == "" {
				t.Fatalf("missing response envelope: %+v", response)
			}
			if response.Base != nil {
				t.Fatalf("snapshot base = %+v, want nil", response.Base)
			}
			var snapshot hubapi.NavigationSnapshot
			if err := json.Unmarshal(response.Data, &snapshot); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if len(snapshot.Entities) != tt.wantEntities {
				t.Fatalf("entities = %d, want %d: %s", len(snapshot.Entities), tt.wantEntities, response.Data)
			}
			if len(snapshot.Containers) == 0 {
				t.Fatalf("snapshot has no containers: %s", response.Data)
			}
		})
	}
}

func TestHubNavigationReadPageDefaultsAndCaps(t *testing.T) {
	zero := uint32(0)
	tests := []struct {
		name   string
		params appwire.NavigationReadParams
		want   navigationResourceKey
	}{
		{
			name:   "section defaults",
			params: appwire.NavigationReadParams{Resource: "section", Section: "live"},
			want:   navigationResourceKey{Kind: navigationResourceLive, Limit: maxNavigationSectionRows},
		},
		{
			name:   "section explicit zero offset",
			params: appwire.NavigationReadParams{Resource: "section", Section: "live", Offset: &zero},
			want:   navigationResourceKey{Kind: navigationResourceLive, Limit: maxNavigationSectionRows},
		},
		{
			name:   "pin catalog defaults",
			params: appwire.NavigationReadParams{Resource: "pin_catalog"},
			want:   navigationResourceKey{Kind: navigationResourcePinCatalog, Limit: maxNavigationCatalogRows},
		},
		{
			name:   "catalog explicit bounded limit",
			params: appwire.NavigationReadParams{Resource: "catalog", Catalog: "projects", Limit: uint32Pointer(7)},
			want:   navigationResourceKey{Kind: navigationResourceProjects, Limit: 7},
		},
		{
			name:   "project page defaults",
			params: appwire.NavigationReadParams{Resource: "project_page", ProjectKey: "p1", Tier: "recent"},
			want:   navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "recent", Limit: maxNavigationSectionRows},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := navigationReadKey(tt.params)
			if err != nil {
				t.Fatalf("navigationReadKey: %v", err)
			}
			if got != tt.want {
				t.Fatalf("key = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHubNavigationReadNormalizesPagingAndRejectsInvalidCombinations(t *testing.T) {
	service := newNavigationReadTestService(t)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, service)

	zero := uint32(0)
	valid := []struct {
		name   string
		params appwire.NavigationReadParams
	}{
		{
			name:   "explicit zero offset",
			params: appwire.NavigationReadParams{Resource: "section", Section: "live", Offset: &zero},
		},
		{
			name:   "explicit zero offset and default limit",
			params: appwire.NavigationReadParams{Resource: "catalog", Catalog: "projects", Offset: &zero},
		},
		{
			name: "explicit bounded limit",
			params: appwire.NavigationReadParams{
				Resource: "project_page", ProjectKey: "p1", Tier: "recent", Limit: uint32Pointer(7),
			},
		},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			response := dispatchNavigationRead(t, server, tt.params)
			if response.Status != "ok" {
				t.Fatalf("status = %q, want ok", response.Status)
			}
		})
	}

	invalid := []struct {
		name   string
		params appwire.NavigationReadParams
	}{
		{
			name:   "unknown resource",
			params: appwire.NavigationReadParams{Resource: "threads"},
		},
		{
			name:   "missing section",
			params: appwire.NavigationReadParams{Resource: "section"},
		},
		{
			name:   "invalid section",
			params: appwire.NavigationReadParams{Resource: "section", Section: "archived"},
		},
		{
			name:   "section extraneous project",
			params: appwire.NavigationReadParams{Resource: "section", Section: "live", ProjectKey: "p1"},
		},
		{
			name:   "missing pin section id",
			params: appwire.NavigationReadParams{Resource: "pin_section"},
		},
		{
			name:   "invalid catalog",
			params: appwire.NavigationReadParams{Resource: "catalog", Catalog: "all"},
		},
		{
			name:   "missing project key",
			params: appwire.NavigationReadParams{Resource: "project"},
		},
		{
			name:   "unpaged project with tier",
			params: appwire.NavigationReadParams{Resource: "project", ProjectKey: "p1", Tier: "current"},
		},
		{
			name:   "missing project page tier",
			params: appwire.NavigationReadParams{Resource: "project_page", ProjectKey: "p1"},
		},
		{
			name:   "invalid project page tier",
			params: appwire.NavigationReadParams{Resource: "project_page", ProjectKey: "p1", Tier: "all"},
		},
		{
			name:   "explicit zero limit",
			params: appwire.NavigationReadParams{Resource: "section", Section: "live", Limit: &zero},
		},
		{
			name: "section limit above cap",
			params: appwire.NavigationReadParams{
				Resource: "section", Section: "live", Limit: uint32Pointer(maxNavigationSectionRows + 1),
			},
		},
		{
			name:   "unpaged manifest with zero offset",
			params: appwire.NavigationReadParams{Resource: "manifest", Offset: &zero},
		},
		{
			name:   "unpaged location with zero limit",
			params: appwire.NavigationReadParams{Resource: "location", Ref: navigationTestSessionID, Limit: &zero},
		},
		{
			name:   "location with malformed ref",
			params: appwire.NavigationReadParams{Resource: "location", Ref: "not a ref"},
		},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dispatchNavigationReadResult(t, server, tt.params)
			assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
		})
	}

	_, err := dispatchNavigationReadRaw(t, server, `{"resource":"section","section":"live","offset":4294967296}`)
	assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	for _, raw := range []string{
		`{"resource":"section","section":"live","projectKey":""}`,
		`{"resource":"section","section":"live","future":true}`,
		`null`,
	} {
		t.Run("reject malformed field set "+raw, func(t *testing.T) {
			_, err := dispatchNavigationReadRaw(t, server, raw)
			assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
		})
	}
}

func TestHubNavigationReadRejectsIncompleteBaseBeforeService(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, nil)

	for name, raw := range map[string]string{
		"revision omitted": `{"resource":"manifest","representationVersion":2,"base":{"generationId":"g","etag":"e"}}`,
		"revision null":    `{"resource":"manifest","representationVersion":2,"base":{"generationId":"g","revision":null,"etag":"e"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := dispatchNavigationReadRaw(t, server, raw)
			assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
		})
	}
}

func TestHubNavigationInvalidParamsDoNotExposeRequestContent(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, nil)

	for _, test := range []struct {
		name     string
		sentinel string
		raw      string
	}{
		{
			name:     "decode failure",
			sentinel: "decode-private-sentinel",
			raw:      `{"resource":"manifest","decode-private-sentinel":true}`,
		},
		{
			name:     "semantic validation failure",
			sentinel: "validation-private-sentinel",
			raw:      `{"resource":"validation-private-sentinel"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := dispatchNavigationReadRaw(t, server, test.raw)
			if err == nil {
				t.Fatal("error = nil, want invalid params")
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("error = %T %v, want appwire.WireError", err, err)
			}
			if wire.Code != appwire.CodeInvalidParams || wire.Message != "invalid navigation params" {
				t.Fatalf("wire error = %+v, want stable invalid params code/message", wire)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || data.EvenerErrorInfo != appwire.ErrorInvalidParams {
				t.Fatalf("wire data = %#v, want invalid params error info", wire.Data)
			}
			encoded, marshalErr := json.Marshal(wire)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if bytes.Contains(encoded, []byte(test.sentinel)) || strings.Contains(wire.Message, test.sentinel) {
				t.Fatalf("serialized wire error exposed sentinel %q: %s", test.sentinel, encoded)
			}
		})
	}
}

func TestHubNavigationInternalErrorsAreLoggedAndRedacted(t *testing.T) {
	const sentinel = "NAV_PRIVATE_SENTINEL::/private/navigation/path::local:secret-ref::secret-key"
	var logs []string
	source := newTestNavigationSource(testNavigationNow())
	source.err = errors.New("capture failed at " + sentinel)
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName: "test",
		Logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	registerNavigationReadHandler(server, newTestNavigationService(t, source))

	_, err := dispatchNavigationReadResult(t, server, appwire.NavigationReadParams{Resource: "manifest"})
	assertNavigationWireError(t, err, appwire.CodeInternalError, appwire.ErrorInternal)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Message != "navigation read failed" {
		t.Errorf("wire message = %q, want fixed redacted message", wire.Message)
	}
	encoded, marshalErr := json.Marshal(wire)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	const wantWire = `{"code":-32603,"message":"navigation read failed","data":{"evenerErrorInfo":"internal"}}`
	if string(encoded) != wantWire {
		t.Errorf("serialized wire error = %s, want %s", encoded, wantWire)
	}
	if bytes.Contains(encoded, []byte(sentinel)) {
		t.Errorf("serialized wire error exposed sentinel %q: %s", sentinel, encoded)
	}
	data, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	const wantData = `{"evenerErrorInfo":"internal"}`
	if string(data) != wantData {
		t.Errorf("serialized wire error data = %s, want %s", data, wantData)
	}
	if bytes.Contains(data, []byte(sentinel)) {
		t.Errorf("serialized wire error data exposed sentinel %q: %s", sentinel, data)
	}
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "navigation read failed:") || !strings.Contains(joinedLogs, sentinel) {
		t.Errorf("server diagnostics did not retain internal error details: %q", logs)
	}
}

func TestHubNavigationReadConditionalResponseAndErrorMapping(t *testing.T) {
	service := newNavigationReadTestService(t)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, service)

	first := dispatchNavigationRead(t, server, appwire.NavigationReadParams{Resource: "manifest", RepresentationVersion: 2})
	if first.Status != "ok" || first.Representation != appwire.NavigationRepresentationSnapshot {
		t.Fatalf("initial response = %+v, want ok snapshot", first)
	}
	conditional := appwire.NavigationReadParams{
		Resource:              "manifest",
		RepresentationVersion: 2,
		Base:                  &appwire.NavigationReadBase{GenerationID: first.GenerationID, Revision: first.Revision, ETag: first.ETag},
	}
	notModified := dispatchNavigationRead(t, server, conditional)
	if notModified.Status != "not_modified" || len(notModified.Data) != 0 {
		t.Fatalf("conditional response = %+v, want not_modified without data", notModified)
	}
	if notModified.GenerationID != first.GenerationID || notModified.Revision != first.Revision || notModified.ETag != first.ETag {
		t.Fatalf("conditional envelope = %+v, want %+v", notModified, first)
	}

	v2 := func(params appwire.NavigationReadParams) appwire.NavigationReadParams {
		params.RepresentationVersion = 2
		return params
	}
	var err error
	for _, params := range []appwire.NavigationReadParams{
		{Resource: "project", ProjectKey: "missing"},
		{Resource: "pin_section", SectionID: "missing"},
		{Resource: "location", Ref: "missing"},
	} {
		_, err = dispatchNavigationReadResult(t, server, v2(params))
		assertNavigationWireError(t, err, appwire.CodeUnavailable, appwire.ErrorActionUnavailable)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = dispatchNavigationReadResultWithContext(canceled, t, server, v2(appwire.NavigationReadParams{Resource: "manifest"}))
	assertNavigationWireError(t, err, appwire.CodeUnavailable, appwire.ErrorActionUnavailable)

	failingSource := newTestNavigationSource(testNavigationNow())
	failingSource.err = errors.New("capture failed")
	failingService := newTestNavigationService(t, failingSource)
	failingServer := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(failingServer, failingService)
	_, err = dispatchNavigationReadResult(t, failingServer, v2(appwire.NavigationReadParams{Resource: "manifest"}))
	assertNavigationWireError(t, err, appwire.CodeInternalError, appwire.ErrorInternal)

	changed := dispatchNavigationRead(t, server, v2(appwire.NavigationReadParams{Resource: "manifest"}))
	var snapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(changed.Data, &snapshot); err != nil {
		t.Fatalf("decode v2 snapshot: %v", err)
	}
	if changed.Representation != appwire.NavigationRepresentationSnapshot || len(snapshot.Entities) == 0 && len(snapshot.Containers) == 0 {
		t.Fatalf("response = %+v, want v2 snapshot with entities/containers", changed)
	}
}

func TestHubNavigationReadIsUnavailableWithoutService(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, nil)

	_, err := dispatchNavigationReadResult(t, server, appwire.NavigationReadParams{Resource: "manifest"})
	assertNavigationWireError(t, err, appwire.CodeUnavailable, appwire.ErrorActionUnavailable)
}

func TestHubNavigationReadIsRegisteredOnTestHubWithoutService(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{}, nil)
	if !strings.Contains(strings.Join(server.Router().Methods(), "\n"), appwire.MethodEvenerNavigationRead) {
		t.Fatalf("hub router methods do not include %q", appwire.MethodEvenerNavigationRead)
	}
	_, err := dispatchNavigationReadResult(t, server, appwire.NavigationReadParams{Resource: "manifest"})
	assertNavigationWireError(t, err, appwire.CodeUnavailable, appwire.ErrorActionUnavailable)
}

func TestHubNavigationReadOverAppWireWebSocket(t *testing.T) {
	service := newNavigationReadTestService(t)
	server := newHubAppServerWithNavigation(hubcore.WebConfig{}, nil, service, nil)
	paths := make(chan string, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		server.ServeWebSocket(w, r)
	}))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):]+"/rpc", httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test cleanup
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	response, err := client.NavigationRead(ctx, appwire.NavigationReadParams{Resource: "project", ProjectKey: "p1", RepresentationVersion: 2})
	if err != nil {
		t.Fatalf("navigation read: %v", err)
	}
	if response.Status != "ok" || response.Representation != appwire.NavigationRepresentationSnapshot || response.GenerationID == "" || response.Revision == 0 || response.ETag == "" {
		t.Fatalf("response envelope = %+v", response)
	}
	var snapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(response.Data, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snapshot.Entities) != 2 || len(snapshot.Containers) == 0 {
		t.Fatalf("snapshot = %+v, want 2 entities with containers", snapshot)
	}
	select {
	case path := <-paths:
		if path != "/rpc" {
			t.Fatalf("websocket path = %q, want /rpc", path)
		}
	case <-ctx.Done():
		t.Fatalf("websocket request was not observed: %v", ctx.Err())
	}
}

func newNavigationReadTestService(t *testing.T) *NavigationService {
	t.Helper()
	source := newTestNavigationSource(testNavigationNow())
	source.inputs.Tree.Live = append([]hubcore.TreeNode(nil), source.inputs.Tree.Projects[0].Current...)
	source.inputs.PinSections = []hubcore.PinSection{{ID: "pin-a", Name: "Pinned"}}
	source.inputs.PinAssignments = map[string]hubcore.SessionPin{
		navigationTestSessionID: {SessionID: navigationTestSessionID, SectionID: "pin-a"},
	}
	return newTestNavigationService(t, source)
}

func testNavigationNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

func dispatchNavigationRead(t *testing.T, server *appserver.Server, params appwire.NavigationReadParams) appwire.NavigationReadResponse {
	t.Helper()
	response, err := dispatchNavigationReadResult(t, server, params)
	if err != nil {
		t.Fatalf("navigation read: %v", err)
	}
	return response
}

func dispatchNavigationReadResult(t *testing.T, server *appserver.Server, params appwire.NavigationReadParams) (appwire.NavigationReadResponse, error) {
	t.Helper()
	return dispatchNavigationReadResultWithContext(context.Background(), t, server, params)
}

func dispatchNavigationReadResultWithContext(ctx context.Context, t *testing.T, server *appserver.Server, params appwire.NavigationReadParams) (appwire.NavigationReadResponse, error) {
	t.Helper()
	params.RepresentationVersion = 2
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return dispatchNavigationReadRawContext(ctx, t, server, string(raw))
}

func dispatchNavigationReadRaw(t *testing.T, server *appserver.Server, raw string) (appwire.NavigationReadResponse, error) {
	t.Helper()
	return dispatchNavigationReadRawContext(context.Background(), t, server, raw)
}

func dispatchNavigationReadRawContext(ctx context.Context, t *testing.T, server *appserver.Server, raw string) (appwire.NavigationReadResponse, error) {
	t.Helper()
	result, err := server.Router().Dispatch(ctx, appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerNavigationRead,
		Params: json.RawMessage(raw),
	})
	if err != nil {
		return appwire.NavigationReadResponse{}, err
	}
	response, ok := result.(appwire.NavigationReadResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.NavigationReadResponse", result)
	}
	return response, nil
}

func assertNavigationWireError(t *testing.T, err error, wantCode int, wantInfo appwire.ErrorInfo) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %d", wantCode)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != wantCode {
		t.Fatalf("wire code = %d, want %d (%v)", wire.Code, wantCode, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data = %T, want appwire.ErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != wantInfo {
		t.Fatalf("wire error info = %q, want %q", data.EvenerErrorInfo, wantInfo)
	}
}

func uint32Pointer(value uint32) *uint32 {
	return new(value)
}
