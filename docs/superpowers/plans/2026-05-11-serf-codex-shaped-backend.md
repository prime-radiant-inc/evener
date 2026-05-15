# Serf Codex-Shaped Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Serf's current REST/SSE hub and daemon backend contract with a Codex-shaped JSON-RPC app-server protocol, including Serf extensions for clear, fork-from-turn, tasks, subagents, provider-neutral models, and directory completion.

**Architecture:** Add a new app-wire protocol package and reusable app-server plumbing, convert `serf serve` into an app-wire source, make `serf-hub` a JSON-RPC multiplexer over local daemon sources, then move TUI and browser clients onto the same wire protocol. This is a breaking plan with no REST/SSE or `internal/hubapi` compatibility path.

**Tech Stack:** Go, `net/http`, gorilla-free websocket if the repo already has a websocket dependency, otherwise `nhooyr.io/websocket`; existing Bubble Tea TUI; existing browser assets; existing Serf agent/session packages.

**Spec:** `docs/superpowers/specs/2026-05-11-serf-codex-shaped-backend-design.md`

---

## File Structure

New core protocol packages:

```text
internal/appwire/
  jsonrpc.go              # JSON-RPC envelope types, encode/decode helpers, request IDs
  jsonrpc_test.go
  refs.go                 # source-qualified refs and cursor validation
  refs_test.go
  types.go                # Thread, Turn, ThreadItem, params, responses, Serf extension fields
  errors.go               # JSON-RPC and Serf error helpers
  client.go               # typed client over a transport
  client_test.go
  transport.go            # connection transport interface
  ws_transport.go         # websocket transport
  ws_transport_test.go

internal/appserver/
  server.go               # initialize gate, request routing, notification writes
  server_test.go
  router.go               # method registration and typed dispatch
  router_test.go
  subscriptions.go        # connection-thread subscription tracking
  subscriptions_test.go
  notifier.go             # ordered notification fanout
  notifier_test.go

internal/appsource/
  source.go               # source interface shaped like app-wire methods
  registry.go             # source lookup and ref routing
  registry_test.go
  local_daemon.go         # hub source driver for local serf serve daemons
  local_daemon_test.go
```

Existing packages to modify:

```text
rendezvous/rendezvous.go            # endpoint/protocol/thread identity in rendezvous files
server/server.go                    # replace REST/SSE handler with app-wire runtime handler
server/broadcaster.go               # remove after appserver notifier is in use
agent/events.go                     # keep event payloads; add projection tests in new package
agent/session.go                    # expose stable active turn/item hooks needed by projection
cmd/serf/serve.go                   # start app-wire endpoint and write new rendezvous format
cmd/serf-hub/main.go                # initialize appserver and source registry
cmd/serf-hub/web.go                 # replace /api session handlers with /rpc and static shell
cmd/serf-hub/roster.go              # discover app-wire daemon endpoints, not REST status
cmd/serf-hub/spawn.go               # spawn daemon app-wire source and return ref
cmd/serf-tui/main.go                # connect appwire client
cmd/serf-tui/hub_model.go           # consume Thread/Turn/Item state
cmd/serf-tui/hub_commands.go        # call appwire methods instead of hubapi REST
cmd/serf-tui/sse_client.go          # delete once appwire notifications drive session view
internal/hubapi/*                   # delete after TUI and web no longer import it
```

---

## Task 1: Add `internal/appwire` JSON-RPC Foundation

**Files:**
- Create: `internal/appwire/jsonrpc.go`
- Create: `internal/appwire/jsonrpc_test.go`
- Create: `internal/appwire/errors.go`

- [x] **Step 1: Write failing JSON-RPC tests**

Create `internal/appwire/jsonrpc_test.go`:

```go
package appwire

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":7,"method":"thread/list","params":{"limit":25}}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Kind() != MessageRequest {
		t.Fatalf("Kind=%v, want request", msg.Kind())
	}
	if msg.Request.ID.Int64() != 7 {
		t.Fatalf("id=%v, want 7", msg.Request.ID)
	}
	if msg.Request.Method != "thread/list" {
		t.Fatalf("method=%q", msg.Request.Method)
	}
	if string(msg.Request.Params) != `{"limit":25}` {
		t.Fatalf("params=%s", msg.Request.Params)
	}
}

func TestNotificationRoundTrip(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","method":"thread/status/changed","params":{"threadId":"th_1"}}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Kind() != MessageNotification {
		t.Fatalf("Kind=%v, want notification", msg.Kind())
	}
	if msg.Notification.Method != "thread/status/changed" {
		t.Fatalf("method=%q", msg.Notification.Method)
	}
}

func TestErrorResponseEncoding(t *testing.T) {
	msg := ErrorMessage(NewIntID(3), InvalidParams("threadId is required"))
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if decoded.JSONRPC != "2.0" || decoded.ID != 3 {
		t.Fatalf("decoded envelope=%+v", decoded)
	}
	if decoded.Error.Code != CodeInvalidParams {
		t.Fatalf("code=%d, want %d", decoded.Error.Code, CodeInvalidParams)
	}
}
```

- [x] **Step 2: Run the tests and verify they fail**

Run:

```bash
go test ./internal/appwire
```

Expected: package or type definitions are missing.

- [x] **Step 3: Add JSON-RPC envelope implementation**

Create `internal/appwire/jsonrpc.go`:

```go
package appwire

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type MessageKind int

const (
	MessageInvalid MessageKind = iota
	MessageRequest
	MessageNotification
	MessageResponse
	MessageError
)

type ID struct {
	raw json.RawMessage
}

func NewIntID(v int64) ID {
	return ID{raw: json.RawMessage(strconv.FormatInt(v, 10))}
}

func (id ID) MarshalJSON() ([]byte, error) {
	if len(id.raw) == 0 {
		return []byte("null"), nil
	}
	return id.raw, nil
}

func (id *ID) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return fmt.Errorf("request id must not be null")
	}
	id.raw = append(id.raw[:0], data...)
	return nil
}

func (id ID) Int64() int64 {
	var n int64
	_ = json.Unmarshal(id.raw, &n)
	return n
}

func (id ID) String() string {
	var s string
	if err := json.Unmarshal(id.raw, &s); err == nil {
		return s
	}
	return string(id.raw)
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      ID              `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      ID     `json:"id"`
	Result  any    `json:"result"`
}

type WireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ErrorResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      ID        `json:"id"`
	Error   WireError `json:"error"`
}

type Message struct {
	Request      *Request
	Notification *Notification
	Response     *Response
	Error        *ErrorResponse
}

func (m Message) Kind() MessageKind {
	switch {
	case m.Request != nil:
		return MessageRequest
	case m.Notification != nil:
		return MessageNotification
	case m.Response != nil:
		return MessageResponse
	case m.Error != nil:
		return MessageError
	default:
		return MessageInvalid
	}
}

func (m Message) IDString() string {
	switch {
	case m.Request != nil:
		return m.Request.ID.String()
	case m.Response != nil:
		return m.Response.ID.String()
	case m.Error != nil:
		return m.Error.ID.String()
	default:
		return ""
	}
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.JSONRPC != "2.0" {
		return fmt.Errorf("jsonrpc must be 2.0")
	}
	switch {
	case len(probe.Error) > 0:
		var out ErrorResponse
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		m.Error = &out
	case len(probe.Result) > 0:
		var out Response
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		m.Response = &out
	case probe.Method != "" && len(probe.ID) > 0:
		var out Request
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		m.Request = &out
	case probe.Method != "":
		var out Notification
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		m.Notification = &out
	default:
		return fmt.Errorf("invalid JSON-RPC message")
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	switch {
	case m.Request != nil:
		return json.Marshal(m.Request)
	case m.Notification != nil:
		return json.Marshal(m.Notification)
	case m.Response != nil:
		return json.Marshal(m.Response)
	case m.Error != nil:
		return json.Marshal(m.Error)
	default:
		return nil, fmt.Errorf("invalid JSON-RPC message")
	}
}

func RequestMessage(id ID, method string, params any) Message {
	return Message{Request: &Request{JSONRPC: "2.0", ID: id, Method: method, Params: mustRaw(params)}}
}

func NotificationMessage(method string, params any) Message {
	return Message{Notification: &Notification{JSONRPC: "2.0", Method: method, Params: mustRaw(params)}}
}

func ResponseMessage(id ID, result any) Message {
	return Message{Response: &Response{JSONRPC: "2.0", ID: id, Result: result}}
}

func ErrorMessage(id ID, err WireError) Message {
	return Message{Error: &ErrorResponse{JSONRPC: "2.0", ID: id, Error: err}}
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
```

Create `internal/appwire/errors.go`:

```go
package appwire

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeOverloaded     = -32001
	CodeThreadNotFound = -32010
	CodeSourceNotFound = -32011
	CodeThreadNotLoad  = -32012
	CodeTurnMismatch   = -32013
	CodeUnavailable    = -32014
	CodeProvider       = -32015
)

type ErrorInfo string

const (
	ErrorInvalidParams      ErrorInfo = "invalidParams"
	ErrorThreadNotFound     ErrorInfo = "threadNotFound"
	ErrorSourceNotFound     ErrorInfo = "sourceNotFound"
	ErrorThreadNotLoaded    ErrorInfo = "threadNotLoaded"
	ErrorActiveTurnMismatch ErrorInfo = "activeTurnMismatch"
	ErrorActionUnavailable  ErrorInfo = "actionUnavailable"
	ErrorProviderUnavailable ErrorInfo = "providerUnavailable"
)

type ErrorData struct {
	SerfErrorInfo ErrorInfo `json:"serfErrorInfo"`
	SourceID      string    `json:"sourceId,omitempty"`
	ThreadID      string    `json:"threadId,omitempty"`
	Retryable     bool      `json:"retryable"`
}

func InvalidParams(message string) WireError {
	return WireError{Code: CodeInvalidParams, Message: message, Data: ErrorData{SerfErrorInfo: ErrorInvalidParams}}
}

func MethodNotFound(method string) WireError {
	return WireError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

func InternalError(message string) WireError {
	return WireError{Code: CodeInternalError, Message: message}
}
```

- [x] **Step 4: Run tests and commit**

Run:

```bash
go test ./internal/appwire
```

Expected: PASS.

Commit:

```bash
git add internal/appwire/jsonrpc.go internal/appwire/jsonrpc_test.go internal/appwire/errors.go
git commit -m "feat(appwire): add JSON-RPC envelope types"
```

---

## Task 2: Define App-Wire Types, Refs, And Method Constants

**Files:**
- Create: `internal/appwire/types.go`
- Create: `internal/appwire/refs.go`
- Create: `internal/appwire/refs_test.go`

- [x] **Step 1: Write failing ref tests**

Create `internal/appwire/refs_test.go`:

```go
package appwire

import "testing"

func TestRefRoundTrip(t *testing.T) {
	ref := Ref{SourceID: "local", ThreadID: "th_01HX"}
	if ref.String() != "local:th_01HX" {
		t.Fatalf("String=%q", ref.String())
	}
	parsed, err := ParseRef("local:th_01HX")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if parsed.SourceID != "local" || parsed.ThreadID != "th_01HX" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseRefRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"", "local", "local:", ":th_1", "local:../x", "local:with space"} {
		if _, err := ParseRef(raw); err == nil {
			t.Fatalf("ParseRef(%q) succeeded", raw)
		}
	}
}
```

- [x] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/appwire
```

Expected: missing `Ref` and `ParseRef`.

- [x] **Step 3: Add refs and core types**

Create `internal/appwire/refs.go`:

```go
package appwire

import (
	"fmt"
	"regexp"
	"strings"
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._~:-]+$`)

type Ref struct {
	SourceID string `json:"sourceId"`
	ThreadID string `json:"threadId"`
}

func (r Ref) String() string {
	if r.SourceID == "" || r.ThreadID == "" {
		return ""
	}
	return r.SourceID + ":" + r.ThreadID
}

func ParseRef(raw string) (Ref, error) {
	if raw == "" || !refPattern.MatchString(raw) {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	source, threadID, ok := strings.Cut(raw, ":")
	if !ok || source == "" || threadID == "" || strings.Contains(threadID, "..") {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	return Ref{SourceID: source, ThreadID: threadID}, nil
}
```

Create `internal/appwire/types.go` with the initial request/response and notification types:

```go
package appwire

const (
	MethodInitialize           = "initialize"
	MethodThreadList           = "thread/list"
	MethodThreadRead           = "thread/read"
	MethodThreadTurnsList      = "thread/turns/list"
	MethodThreadTurnItemsList  = "thread/turns/items/list"
	MethodThreadStart          = "thread/start"
	MethodThreadResume         = "thread/resume"
	MethodThreadFork           = "thread/fork"
	MethodThreadClear          = "thread/clear"
	MethodThreadModelSet       = "thread/model/set"
	MethodThreadCompactStart   = "thread/compact/start"
	MethodTurnStart            = "turn/start"
	MethodTurnSteer            = "turn/steer"
	MethodTurnInterrupt        = "turn/interrupt"
	MethodSerfTasksList        = "serf/tasks/list"
	MethodSerfDirsComplete     = "serf/dirs/complete"
	MethodModelList            = "model/list"
	NotifyThreadStarted        = "thread/started"
	NotifyThreadStatusChanged  = "thread/status/changed"
	NotifyTurnStarted          = "turn/started"
	NotifyTurnCompleted        = "turn/completed"
	NotifyItemStarted          = "item/started"
	NotifyItemCompleted        = "item/completed"
	NotifyAgentMessageDelta    = "item/agentMessage/delta"
	NotifySerfContextPressure  = "serf/thread/contextPressure/updated"
	NotifySerfTaskUpdated      = "serf/task/updated"
)

type InitializeParams struct {
	ClientInfo   ClientInfo   `json:"clientInfo"`
	Capabilities Capabilities `json:"capabilities"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Capabilities struct {
	ExperimentalAPI          bool     `json:"experimentalApi"`
	OptOutNotificationNames  []string `json:"optOutNotificationMethods,omitempty"`
}

type InitializeResponse struct {
	ServerInfo      ServerInfo      `json:"serverInfo"`
	ProtocolVersion string          `json:"protocolVersion"`
	SourceID        string          `json:"sourceId"`
	Features        FeatureSet      `json:"features"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type FeatureSet struct {
	ThreadList        bool `json:"threadList"`
	ThreadTurnsList   bool `json:"threadTurnsList"`
	TurnStart         bool `json:"turnStart"`
	TurnSteer         bool `json:"turnSteer"`
	ThreadClear       bool `json:"threadClear"`
	ForkFromTurn      bool `json:"forkFromTurn"`
	Tasks             bool `json:"tasks"`
	ModelList         bool `json:"modelList"`
	DirectoryComplete bool `json:"directoryComplete"`
}

type Thread struct {
	ID            string       `json:"id"`
	SessionID     string       `json:"sessionId"`
	ForkedFromID  string       `json:"forkedFromId,omitempty"`
	Preview       string       `json:"preview"`
	Ephemeral     bool         `json:"ephemeral"`
	ModelProvider string       `json:"modelProvider"`
	CreatedAt     int64        `json:"createdAt"`
	UpdatedAt     int64        `json:"updatedAt"`
	Status        ThreadStatus `json:"status"`
	Path          string       `json:"path,omitempty"`
	CWD           string       `json:"cwd"`
	CLIVersion    string       `json:"cliVersion"`
	Source        string       `json:"source"`
	ThreadSource  string       `json:"threadSource,omitempty"`
	AgentNickname string       `json:"agentNickname,omitempty"`
	AgentRole     string       `json:"agentRole,omitempty"`
	GitInfo       *GitInfo     `json:"gitInfo,omitempty"`
	Name          string       `json:"name,omitempty"`
	Turns         []Turn       `json:"turns"`
	Serf          SerfThread   `json:"serf"`
}

type GitInfo struct {
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	OriginURL string `json:"originUrl,omitempty"`
}

type ThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

type SerfThread struct {
	Ref             string             `json:"ref"`
	Profile         string             `json:"profile,omitempty"`
	ContextPressure float64            `json:"contextPressure,omitempty"`
	Capabilities    ThreadCapabilities `json:"capabilities"`
}

type ThreadCapabilities struct {
	Send         bool `json:"send"`
	Steer        bool `json:"steer"`
	Interrupt    bool `json:"interrupt"`
	Compact      bool `json:"compact"`
	Clear        bool `json:"clear"`
	ForkFromTurn bool `json:"forkFromTurn"`
	Shutdown     bool `json:"shutdown"`
	ChangeModel  bool `json:"changeModel"`
}

type Turn struct {
	ID          string       `json:"id"`
	Items       []ThreadItem `json:"items"`
	ItemsView   string       `json:"itemsView"`
	Status      string       `json:"status"`
	Error       *TurnError   `json:"error,omitempty"`
	StartedAt   *int64       `json:"startedAt"`
	CompletedAt *int64       `json:"completedAt"`
	DurationMS  *int64       `json:"durationMs"`
}

type TurnError struct {
	Message string `json:"message"`
}

type ThreadItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
}

type ThreadListParams struct {
	Cursor          string   `json:"cursor,omitempty"`
	Limit           int      `json:"limit,omitempty"`
	SortKey         string   `json:"sortKey,omitempty"`
	SortDirection   string   `json:"sortDirection,omitempty"`
	SearchTerm      string   `json:"searchTerm,omitempty"`
	Statuses        []string `json:"statuses,omitempty"`
	SourceIDs       []string `json:"sourceIds,omitempty"`
	IncludeSubagents bool    `json:"includeSubagents,omitempty"`
}

type ThreadListResponse struct {
	Data            []Thread `json:"data"`
	NextCursor      string   `json:"nextCursor,omitempty"`
	BackwardsCursor string   `json:"backwardsCursor,omitempty"`
}

type ThreadReadParams struct {
	ThreadID     string `json:"threadId,omitempty"`
	Ref          string `json:"ref,omitempty"`
	IncludeTurns bool   `json:"includeTurns"`
	ItemsView    string `json:"itemsView,omitempty"`
}

type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
}
```

- [x] **Step 4: Run tests and commit**

Run:

```bash
go test ./internal/appwire
```

Expected: PASS.

Commit:

```bash
git add internal/appwire/types.go internal/appwire/refs.go internal/appwire/refs_test.go
git commit -m "feat(appwire): define thread refs and core types"
```

---

## Task 3: Add App-Wire Client And Websocket Transport

**Files:**
- Create: `internal/appwire/transport.go`
- Create: `internal/appwire/client.go`
- Create: `internal/appwire/client_test.go`
- Create: `internal/appwire/ws_transport.go`
- Create: `internal/appwire/ws_transport_test.go`
- Modify: `go.mod`

- [x] **Step 1: Write failing typed client test**

Create `internal/appwire/client_test.go`:

```go
package appwire

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type memoryTransport struct {
	writes chan Message
	reads  chan Message
}

func newMemoryTransport() *memoryTransport {
	return &memoryTransport{
		writes: make(chan Message, 8),
		reads:  make(chan Message, 8),
	}
}

func (m *memoryTransport) Send(_ context.Context, msg Message) error {
	m.writes <- msg
	return nil
}

func (m *memoryTransport) Recv(ctx context.Context) (Message, error) {
	select {
	case msg := <-m.reads:
		return msg, nil
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

func (m *memoryTransport) Close() error { return nil }

func TestClientRoutesResponsesAndNotifications(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	done := make(chan struct {
		resp ThreadListResponse
		err  error
	}, 1)
	go func() {
		resp, err := client.ThreadList(ctx, ThreadListParams{Limit: 10})
		done <- struct {
			resp ThreadListResponse
			err  error
		}{resp: resp, err: err}
	}()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if written.Request.Method != MethodThreadList {
		t.Fatalf("method=%q", written.Request.Method)
	}

	transport.reads <- NotificationMessage(NotifyThreadStatusChanged, map[string]string{"threadId": "th_1"})
	transport.reads <- ResponseMessage(written.Request.ID, ThreadListResponse{Data: []Thread{{ID: "th_1", Source: "serf"}}})

	select {
	case notif := <-client.Notifications():
		if notif.Method != NotifyThreadStatusChanged {
			t.Fatalf("notification method=%q", notif.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not routed")
	}

	var result struct {
		resp ThreadListResponse
		err  error
	}
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
	if result.err != nil {
		t.Fatalf("ThreadList: %v", result.err)
	}
	if len(result.resp.Data) != 1 || result.resp.Data[0].ID != "th_1" {
		t.Fatalf("resp=%+v", result.resp)
	}

	var params ThreadListParams
	if err := json.Unmarshal(written.Request.Params, &params); err != nil {
		t.Fatalf("params decode: %v", err)
	}
	if params.Limit != 10 {
		t.Fatalf("limit=%d, want 10", params.Limit)
	}
}
```

- [x] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/appwire
```

Expected: missing `Transport`, `Client`, and `NewClient`.

- [x] **Step 3: Add transport and client**

Create `internal/appwire/transport.go`:

```go
package appwire

import "context"

type Transport interface {
	Send(context.Context, Message) error
	Recv(context.Context) (Message, error)
	Close() error
}
```

Create `internal/appwire/client.go`:

```go
package appwire

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

type Client struct {
	transport     Transport
	nextID        atomic.Int64
	sendMu        sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]chan Message
	notifications chan Notification
}

func NewClient(transport Transport) *Client {
	c := &Client{
		transport:     transport,
		pending:       map[string]chan Message{},
		notifications: make(chan Notification, 128),
	}
	c.nextID.Store(1)
	return c
}

func (c *Client) Start(ctx context.Context) {
	go func() {
		for {
			msg, err := c.transport.Recv(ctx)
			if err != nil {
				c.failPending(err)
				close(c.notifications)
				return
			}
			if msg.Notification != nil {
				c.notifications <- *msg.Notification
				continue
			}
			id := msg.IDString()
			c.pendingMu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.pendingMu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
	}()
}

func (c *Client) Notifications() <-chan Notification {
	return c.notifications
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	id := NewIntID(c.nextID.Add(1) - 1)
	ch := make(chan Message, 1)

	c.pendingMu.Lock()
	c.pending[id.String()] = ch
	c.pendingMu.Unlock()

	c.sendMu.Lock()
	if err := c.transport.Send(ctx, RequestMessage(id, method, params)); err != nil {
		c.sendMu.Unlock()
		c.removePending(id)
		return err
	}
	c.sendMu.Unlock()

	var msg Message
	select {
	case msg = <-ch:
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	}

	if msg.Error != nil {
		return fmt.Errorf("appwire %s: %s", method, msg.Error.Error.Message)
	}
	if msg.Response == nil {
		return fmt.Errorf("appwire %s: expected response", method)
	}
	data, err := json.Marshal(msg.Response.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *Client) removePending(id ID) {
	c.pendingMu.Lock()
	delete(c.pending, id.String())
	c.pendingMu.Unlock()
}

func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- ErrorMessage(NewIntID(0), InternalError(err.Error()))
	}
}

func (c *Client) Initialize(ctx context.Context, params InitializeParams) (InitializeResponse, error) {
	var out InitializeResponse
	err := c.request(ctx, MethodInitialize, params, &out)
	return out, err
}

func (c *Client) ThreadList(ctx context.Context, params ThreadListParams) (ThreadListResponse, error) {
	var out ThreadListResponse
	err := c.request(ctx, MethodThreadList, params, &out)
	return out, err
}

func (c *Client) ThreadRead(ctx context.Context, params ThreadReadParams) (ThreadReadResponse, error) {
	var out ThreadReadResponse
	err := c.request(ctx, MethodThreadRead, params, &out)
	return out, err
}
```

- [x] **Step 4: Add websocket transport**

Use `nhooyr.io/websocket` unless the repo already contains another websocket dependency when this task starts.

Create `internal/appwire/ws_transport.go`:

```go
package appwire

import (
	"context"
	"encoding/json"
	"net/http"

	"nhooyr.io/websocket"
)

type WSTransport struct {
	conn *websocket.Conn
}

func DialWebSocket(ctx context.Context, url string, client *http.Client) (*WSTransport, error) {
	opts := &websocket.DialOptions{HTTPClient: client}
	conn, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	return &WSTransport{conn: conn}, nil
}

func (t *WSTransport) Send(ctx context.Context, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return t.conn.Write(ctx, websocket.MessageText, data)
}

func (t *WSTransport) Recv(ctx context.Context) (Message, error) {
	_, data, err := t.conn.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func (t *WSTransport) Close() error {
	return t.conn.Close(websocket.StatusNormalClosure, "")
}
```

- [x] **Step 5: Add websocket transport test**

Create `internal/appwire/ws_transport_test.go`:

```go
package appwire

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhooyr.io/websocket"
)

func TestWSTransportRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if msg.Request == nil || msg.Request.Method != MethodThreadList {
			t.Errorf("message=%+v", msg)
			return
		}
		out, err := json.Marshal(ResponseMessage(msg.Request.ID, ThreadListResponse{}))
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, out); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := DialWebSocket(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()

	if err := transport.Send(ctx, RequestMessage(NewIntID(1), MethodThreadList, ThreadListParams{})); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := transport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if resp.Response == nil || resp.Response.ID.Int64() != 1 {
		t.Fatalf("resp=%+v", resp)
	}
}
```

- [x] **Step 6: Run tests and tidy**

Run:

```bash
go test ./internal/appwire
go mod tidy
```

Expected: PASS and `go.mod` includes the websocket module if it was new.

- [x] **Step 7: Commit**

```bash
git add internal/appwire/transport.go internal/appwire/client.go internal/appwire/client_test.go internal/appwire/ws_transport.go internal/appwire/ws_transport_test.go go.mod go.sum
git commit -m "feat(appwire): add typed client transport"
```

---

## Task 4: Add App Server Router, Initialize Gate, And Subscriptions

**Files:**
- Create: `internal/appserver/router.go`
- Create: `internal/appserver/router_test.go`
- Create: `internal/appserver/server.go`
- Create: `internal/appserver/server_test.go`
- Create: `internal/appserver/subscriptions.go`
- Create: `internal/appserver/subscriptions_test.go`
- Create: `internal/appserver/notifier.go`
- Create: `internal/appserver/notifier_test.go`

- [x] **Step 1: Write router dispatch test**

Create `internal/appserver/router_test.go`:

```go
package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestRouterDispatchesTypedHandler(t *testing.T) {
	router := NewRouter()
	HandleTyped(router, appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		if params.Limit != 3 {
			t.Fatalf("limit=%d, want 3", params.Limit)
		}
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	raw, _ := json.Marshal(appwire.ThreadListParams{Limit: 3})
	resp, err := router.Dispatch(context.Background(), appwire.Request{
		JSONRPC: "2.0",
		ID:      appwire.NewIntID(1),
		Method:  appwire.MethodThreadList,
		Params:  raw,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	list, ok := resp.(appwire.ThreadListResponse)
	if !ok {
		t.Fatalf("response type=%T", resp)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "th_1" {
		t.Fatalf("list=%+v", list)
	}
}
```

- [x] **Step 2: Implement router**

Create `internal/appserver/router.go`:

```go
package appserver

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/serf/internal/appwire"
)

type HandlerFunc func(context.Context, json.RawMessage) (any, error)

type Router struct {
	handlers map[string]HandlerFunc
}

func NewRouter() *Router {
	return &Router{handlers: map[string]HandlerFunc{}}
}

func (r *Router) Handle(method string, fn HandlerFunc) {
	r.handlers[method] = fn
}

func HandleTyped[P any, R any](r *Router, method string, fn func(context.Context, P) (R, error)) {
	r.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
		}
		return fn(ctx, params)
	})
}

func (r *Router) Dispatch(ctx context.Context, req appwire.Request) (any, error) {
	fn, ok := r.handlers[req.Method]
	if !ok {
		return nil, appwire.MethodNotFound(req.Method)
	}
	out, err := fn(ctx, req.Params)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func WireError(err error) appwire.WireError {
	if we, ok := err.(appwire.WireError); ok {
		return we
	}
	return appwire.InternalError(fmt.Sprint(err))
}
```

- [x] **Step 3: Add server initialize gate tests**

Create `internal/appserver/server_test.go` with tests that send `thread/list` before `initialize` and expect `invalid request`, then send `initialize` and expect success:

```go
package appserver

import (
	"context"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestServerRequiresInitialize(t *testing.T) {
	server := NewServer(ServerConfig{
		ServerName: "serf-hub",
		Version:    "test",
		SourceID:  "local",
	})
	resp := server.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("kind=%v, want error", resp.Kind())
	}
}

func TestServerInitializeAllowsLaterRequests(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	initResp := server.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if initResp.Kind() != appwire.MessageResponse {
		t.Fatalf("init kind=%v", initResp.Kind())
	}
	listResp := server.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if listResp.Kind() != appwire.MessageResponse {
		t.Fatalf("list kind=%v", listResp.Kind())
	}
}
```

- [x] **Step 4: Implement app server**

Create `internal/appserver/server.go`:

```go
package appserver

import (
	"context"

	"primeradiant.com/serf/internal/appwire"
)

type ServerConfig struct {
	ServerName string
	Version    string
	SourceID  string
	Features  appwire.FeatureSet
}

type Server struct {
	cfg         ServerConfig
	router      *Router
	initialized bool
}

func NewServer(cfg ServerConfig) *Server {
	s := &Server{cfg: cfg, router: NewRouter()}
	HandleTyped(s.router, appwire.MethodInitialize, s.initialize)
	return s
}

func (s *Server) Router() *Router {
	return s.router
}

func (s *Server) HandleMessage(ctx context.Context, msg appwire.Message) appwire.Message {
	if msg.Request == nil {
		return appwire.ErrorMessage(appwire.NewIntID(0), appwire.InvalidParams("request message required"))
	}
	req := *msg.Request
	if !s.initialized && req.Method != appwire.MethodInitialize {
		return appwire.ErrorMessage(req.ID, appwire.WireError{Code: appwire.CodeInvalidRequest, Message: "initialize required"})
	}
	result, err := s.router.Dispatch(ctx, req)
	if err != nil {
		return appwire.ErrorMessage(req.ID, WireError(err))
	}
	return appwire.ResponseMessage(req.ID, result)
}

func (s *Server) initialize(context.Context, appwire.InitializeParams) (appwire.InitializeResponse, error) {
	s.initialized = true
	return appwire.InitializeResponse{
		ServerInfo: appwire.ServerInfo{Name: s.cfg.ServerName, Version: s.cfg.Version},
		ProtocolVersion: "serf-appwire-v1",
		SourceID: s.cfg.SourceID,
		Features: s.cfg.Features,
	}, nil
}
```

- [x] **Step 5: Add subscription and notifier packages**

Create focused tests for:

- A connection can subscribe to two thread IDs.
- Removing a connection removes its thread subscriptions.
- A notifier sends notifications in sequence order.
- A notifier can replay notifications after a cursor.

Create `subscriptions.go` and `notifier.go` with small in-memory maps. Use `sync.Mutex` and do not import hub packages.

- [x] **Step 6: Run tests and commit**

Run:

```bash
go test ./internal/appserver
```

Expected: PASS.

Commit:

```bash
git add internal/appserver
git commit -m "feat(appserver): add JSON-RPC router and subscriptions"
```

---

## Task 5: Project Serf Agent Events Into App-Wire Notifications

**Files:**
- Create: `server/appwire_projection.go`
- Create: `server/appwire_projection_test.go`
- Note: projection lives in `server` so `internal/appwire` remains protocol-only and does not import `agent`.

- [x] **Step 1: Write projection tests**

Create `internal/appwire/projection_test.go`:

```go
package appwire

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

func TestProjectAssistantDelta(t *testing.T) {
	projector := NewProjector(Ref{SourceID: "local", ThreadID: "th_1"})
	out := projector.Project(agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		Timestamp: time.Unix(10, 0),
		SessionID: "sess_1",
		Data:      agent.AssistantTextDeltaData{Delta: "hello"},
	})
	if len(out) != 1 {
		t.Fatalf("len=%d, want 1", len(out))
	}
	if out[0].Method != NotifyAgentMessageDelta {
		t.Fatalf("method=%q", out[0].Method)
	}
	if out[0].ThreadID != "th_1" || out[0].Ref != "local:th_1" {
		t.Fatalf("notification=%+v", out[0])
	}
	if out[0].Sequence != 1 {
		t.Fatalf("sequence=%d, want 1", out[0].Sequence)
	}
}

func TestProjectSubagentStart(t *testing.T) {
	projector := NewProjector(Ref{SourceID: "local", ThreadID: "th_1"})
	out := projector.Project(agent.SessionEvent{
		Kind: agent.EventSubagentStart,
		Data: agent.SubagentStartData{AgentID: "agent_1", Task: "check tests"},
	})
	if len(out) == 0 {
		t.Fatal("expected notifications")
	}
	if out[0].Method != "serf/subagent/started" {
		t.Fatalf("method=%q", out[0].Method)
	}
}
```

- [x] **Step 2: Implement projector**

Create `internal/appwire/projection.go`:

```go
package appwire

import "primeradiant.com/serf/agent"

type ServerNotification struct {
	Method         string `json:"method"`
	SourceID       string `json:"sourceId"`
	ThreadID       string `json:"threadId"`
	Ref            string `json:"ref"`
	TurnID         string `json:"turnId,omitempty"`
	ItemID         string `json:"itemId,omitempty"`
	Sequence       uint64 `json:"sequence"`
	SourceSequence uint64 `json:"sourceSequence,omitempty"`
	Payload        any    `json:"payload,omitempty"`
}

type Projector struct {
	ref      Ref
	sequence uint64
	turnID   string
	itemID   string
}

func NewProjector(ref Ref) *Projector {
	return &Projector{ref: ref}
}

func (p *Projector) Project(event agent.SessionEvent) []ServerNotification {
	p.sequence++
	base := ServerNotification{
		SourceID: p.ref.SourceID,
		ThreadID: p.ref.ThreadID,
		Ref:      p.ref.String(),
		Sequence: p.sequence,
		Payload:  event.Data,
	}
	switch event.Kind {
	case agent.EventSessionStart:
		base.Method = NotifyThreadStarted
	case agent.EventUserInput:
		base.Method = NotifyItemCompleted
	case agent.EventAssistantTextStart:
		base.Method = NotifyItemStarted
	case agent.EventAssistantTextDelta:
		base.Method = NotifyAgentMessageDelta
	case agent.EventAssistantTextEnd:
		base.Method = NotifyItemCompleted
	case agent.EventToolCallStart:
		base.Method = NotifyItemStarted
	case agent.EventToolCallOutputDelta:
		base.Method = "item/serfToolCall/outputDelta"
	case agent.EventToolCallEnd:
		base.Method = NotifyItemCompleted
	case agent.EventContextCompaction:
		base.Method = NotifySerfContextPressure
	case agent.EventSubagentStart:
		base.Method = "serf/subagent/started"
	case agent.EventSubagentEnd:
		base.Method = "serf/subagent/completed"
	case agent.EventPluginLoaded:
		base.Method = "serf/plugin/loaded"
	case agent.EventHookStart:
		base.Method = "serf/hook/started"
	case agent.EventHookEnd:
		base.Method = "serf/hook/completed"
	case agent.EventCommunicate:
		base.Method = "serf/communicate/requested"
	case agent.EventWarning:
		base.Method = "warning"
	case agent.EventError:
		base.Method = "error"
	case agent.EventSessionEnd:
		base.Method = NotifyThreadStatusChanged
	default:
		base.Method = "serf/event"
	}
	return []ServerNotification{base}
}
```

- [x] **Step 3: Keep stable turn and item IDs in the projector**

`agent.SessionEvent` does not currently carry app-wire turn IDs, so `server.AppEventProjector` owns stable per-thread turn and item IDs while projecting the event stream. No `agent/session.go` change is needed for this task.

- [x] **Step 4: Run tests**

Run:

```bash
go test ./server
```

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add server/appwire_projection.go server/appwire_projection_test.go
git commit -m "feat(appwire): project serf events into app notifications"
```

---

## Task 6: Replace `serf serve` REST/SSE Runtime With App-Wire Endpoint

**Files:**
- Modify: `server/server.go`
- Delete in Task 11: `server/broadcaster.go`
- Delete in Task 11: `server/broadcaster_test.go`
- Modify: `server/server_test.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf/serve_test.go`
- Modify: `rendezvous/rendezvous.go`
- Modify: `rendezvous/rendezvous_test.go`

- [x] **Step 1: Add failing rendezvous test for app-wire endpoint**

Update `rendezvous/rendezvous_test.go` with:

```go
func TestEntryRoundTripIncludesAppWireEndpoint(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{
		PID:       123,
		Protocol:  "serf-appwire-v1",
		Endpoint:  "ws://127.0.0.1:49152/rpc",
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	}
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(entries))
	}
	if entries[0].Protocol != "serf-appwire-v1" || entries[0].Endpoint == "" || entries[0].ThreadID != "th_1" {
		t.Fatalf("entry=%+v", entries[0])
	}
}
```

- [x] **Step 2: Add app-wire fields to rendezvous entry**

Modify `rendezvous/rendezvous.go`:

```go
type Entry struct {
	PID           int       `json:"pid"`
	Protocol      string    `json:"protocol"`
	Endpoint      string    `json:"endpoint"`
	SourceID      string    `json:"source_id"`
	ThreadID      string    `json:"thread_id"`
	SessionID     string    `json:"session_id"`
	WorkingDir    string    `json:"working_dir,omitempty"`
	StateDir      string    `json:"state_dir,omitempty"`
	Agent         string    `json:"agent,omitempty"`
	Model         string    `json:"model,omitempty"`
	ModelProvider string    `json:"model_provider,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	SpawnedBy     string    `json:"spawned_by,omitempty"`
}
```

The app-wire fields are present now. Removing old `Address` and `Provider` usage is deferred until the hub source driver and clients are migrated, then completed in Task 11.

- [x] **Step 3: Add app-wire runtime server tests**

Rewrite `server/server_test.go` around app-wire behavior:

- `initialize` succeeds.
- `thread/read` returns the runtime thread.
- `turn/start` writes to `InputCh`.
- `turn/steer` rejects a mismatched active turn ID.
- `turn/interrupt` invokes the cancel function.

Use in-memory `appwire.Transport` from Task 3 for tests. Legacy REST/SSE tests remain until the old runtime paths are removed in Task 11.

- [x] **Step 4: Implement app-wire runtime handler**

In `server/server.go`, add the appserver router and expose it through `/rpc`. Full removal of `http.ServeMux` route setup happens in Task 11 after hub and client migration.

```go
func NewAppServer(cfg ServerConfig, runtime Runtime) *appserver.Server
```

Define `Runtime` in `server/server.go`:

```go
type Runtime interface {
	ThreadRead(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
	TurnStart(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	TurnSteer(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error)
	TurnInterrupt(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
	ThreadCompactStart(context.Context, appwire.ThreadCompactStartParams) (appwire.ThreadCompactStartResponse, error)
	ThreadClear(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error)
	ThreadModelSet(context.Context, appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error)
	ModelList(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error)
	TasksList(context.Context, appwire.TasksListParams) (appwire.TasksListResponse, error)
}
```

Register each method with `appserver.Router`.

- [x] **Step 5: Update `cmd/serf serve`**

Modify `cmd/serf/serve.go` so it:

- Allocates an app-wire endpoint instead of REST endpoint.
- Builds a `server.Runtime` around the existing `agent.Session`.
- Serves websocket `/rpc`.
- Writes rendezvous with `Protocol`, `Endpoint`, `SourceID`, `ThreadID`, and `SessionID`.
- Leaves REST endpoint route setup in place until Task 11 removes old clients and tests.

- [x] **Step 6: Run tests**

Run:

```bash
go test ./rendezvous ./server ./cmd/serf
```

Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add rendezvous/rendezvous.go rendezvous/rendezvous_test.go server cmd/serf
git commit -m "feat(serve): expose appwire runtime endpoint"
```

---

## Task 7: Add Hub Source Registry And Local Daemon Source Driver

**Files:**
- Create: `internal/appsource/source.go`
- Create: `internal/appsource/registry.go`
- Create: `internal/appsource/registry_test.go`
- Create: `internal/appsource/local_daemon.go`
- Create: `internal/appsource/local_daemon_test.go`
- Modify: `cmd/serf-hub/roster.go`
- Modify: `cmd/serf-hub/roster_test.go`

- [ ] **Step 1: Write source registry tests**

Create `internal/appsource/registry_test.go`:

```go
package appsource

import (
	"context"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

type fakeSource struct{ id string }

func (f fakeSource) ID() string { return f.id }
func (f fakeSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
}

func TestRegistryRoutesByRefSource(t *testing.T) {
	reg := NewRegistry()
	reg.Add(fakeSource{id: "local"})
	src, err := reg.SourceForRef("local:th_1")
	if err != nil {
		t.Fatalf("SourceForRef: %v", err)
	}
	if src.ID() != "local" {
		t.Fatalf("source=%q", src.ID())
	}
}
```

- [ ] **Step 2: Implement registry and source interfaces**

Create `internal/appsource/source.go`:

```go
package appsource

import (
	"context"

	"primeradiant.com/serf/internal/appwire"
)

type Source interface {
	ID() string
	ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error)
	ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
	StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error)
	ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error)
	ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error)
	StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error)
	InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
}
```

Create `internal/appsource/registry.go`:

```go
package appsource

import (
	"fmt"
	"sync"

	"primeradiant.com/serf/internal/appwire"
)

type Registry struct {
	mu      sync.RWMutex
	sources map[string]Source
}

func NewRegistry() *Registry {
	return &Registry{sources: map[string]Source{}}
}

func (r *Registry) Add(source Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[source.ID()] = source
}

func (r *Registry) Source(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src, ok := r.sources[id]
	return src, ok
}

func (r *Registry) SourceForRef(raw string) (Source, error) {
	ref, err := appwire.ParseRef(raw)
	if err != nil {
		return nil, err
	}
	src, ok := r.Source(ref.SourceID)
	if !ok {
		return nil, fmt.Errorf("source not found: %s", ref.SourceID)
	}
	return src, nil
}
```

- [ ] **Step 3: Update hub roster**

Modify `cmd/serf-hub/roster.go` so it reads rendezvous files with `Endpoint`, `Protocol`, `ThreadID`, and `SessionID`. Remove `/status` probing. A live daemon is present if:

- The rendezvous file exists.
- `Protocol == "serf-appwire-v1"`.
- `Endpoint` is non-empty.
- `ThreadID` is non-empty.

- [ ] **Step 4: Add local daemon source**

Create `internal/appsource/local_daemon.go`. It should:

- Hold a roster reader.
- Dial daemon `Endpoint` with `appwire.DialWebSocket`.
- Call daemon app-wire methods.
- Return source-qualified refs with `SourceID == "local"`.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
go test ./internal/appsource ./cmd/serf-hub
```

Expected: PASS.

Commit:

```bash
git add internal/appsource cmd/serf-hub/roster.go cmd/serf-hub/roster_test.go
git commit -m "feat(hub): route local daemon appwire sources"
```

---

## Task 8: Convert `serf-hub` To Public `/rpc`

**Files:**
- Modify: `cmd/serf-hub/main.go`
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-hub/spawn.go`
- Modify: `cmd/serf-hub/spawn_test.go`
- Modify: `cmd/serf-hub/web_test.go`
- Delete after clients move: `internal/hubapi/client.go`
- Delete after clients move: `internal/hubapi/types.go`
- Delete after clients move: `internal/hubapi/refs.go`

- [ ] **Step 1: Write hub RPC test**

In `cmd/serf-hub/web_test.go`, add a test that starts the hub handler, dials `/rpc`, sends `initialize`, sends `thread/list`, and receives a JSON-RPC response.

Use this request body over websocket:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"test","version":"test"},"capabilities":{}}}
```

Expected response has `protocolVersion == "serf-appwire-v1"`.

- [ ] **Step 2: Wire appserver into hub**

Modify `cmd/serf-hub/main.go` to construct:

- `appsource.Registry`
- local daemon source
- `appserver.Server`
- method handlers that route by ref or source

The hub method handlers should implement:

- `thread/list`
- `thread/read`
- `thread/start`
- `thread/resume`
- `thread/fork`
- `thread/clear`
- `turn/start`
- `turn/steer`
- `turn/interrupt`
- `thread/compact/start`
- `thread/model/set`
- `serf/tasks/list`
- `serf/dirs/complete`
- `model/list`

- [ ] **Step 3: Replace `/api/*` session handlers**

Modify `cmd/serf-hub/web.go`:

- Keep static assets and page shell.
- Add `mux.HandleFunc("/rpc", s.handleRPC)`.
- Remove `/api/spawn`, `/api/models`, `/api/dirs`, `/api/search`, `/api/health`, `/api/tree`, `/api/spawn-schema`, and `/api/sessions/`.
- Remove SSE replay endpoints from the public surface.

- [ ] **Step 4: Update spawn**

Modify `cmd/serf-hub/spawn.go` so spawn starts `serf serve` with app-wire endpoint support and returns `appwire.ThreadStartResponse`.

- [ ] **Step 5: Run hub tests**

Run:

```bash
go test ./cmd/serf-hub
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub internal/appsource
git commit -m "feat(hub): expose appwire rpc"
```

---

## Task 9: Move `serf-tui` To App-Wire Client

**Files:**
- Modify: `cmd/serf-tui/main.go`
- Modify: `cmd/serf-tui/hub_start.go`
- Modify: `cmd/serf-tui/hub_commands.go`
- Modify: `cmd/serf-tui/hub_model.go`
- Modify: `cmd/serf-tui/hub_model_test.go`
- Delete: `cmd/serf-tui/sse_client.go`
- Delete: `cmd/serf-tui/sse_client_test.go`
- Modify or delete: `internal/hubapi/client_test.go`

- [ ] **Step 1: Write TUI client command tests**

Update `cmd/serf-tui/hub_commands_test.go` or create it if missing. Use a fake appwire client and verify:

- Dashboard fetch calls `ThreadList`.
- Session open calls `ThreadRead`.
- Idle send calls `TurnStart`.
- Active send calls `TurnSteer` with the active turn ID.
- Interrupt calls `TurnInterrupt`.

- [ ] **Step 2: Update hub startup**

Modify `cmd/serf-tui/hub_start.go`:

- Keep address normalization and auto-start behavior.
- Change health probe to websocket `/rpc` initialize.
- Return `*appwire.Client` instead of `*hubapi.Client`.

- [ ] **Step 3: Update commands**

Modify `cmd/serf-tui/hub_commands.go` to call app-wire methods. Replace:

- `fetchHubTree` with `fetchThreadList`.
- `fetchHubSession` with `fetchThreadRead`.
- `sendHubInput` with `sendTurnStart` or `sendTurnSteer`.
- `sendHubAction("interrupt")` with `turn/interrupt`.
- `sendHubClear` with `thread/clear`.
- `sendHubFork` with Serf-extended `thread/fork`.
- `fetchHubModels` with `model/list`.

- [ ] **Step 4: Update model reducer**

Modify `cmd/serf-tui/hub_model.go` so `hubModel` stores:

```go
threads []appwire.Thread
selectedThread appwire.Thread
activeTurnID string
itemsByID map[string]appwire.ThreadItem
```

Apply notifications from app-wire instead of SSE events. Keep existing chat message rendering by converting app-wire `ThreadItem` values into `chatMessage`.

- [ ] **Step 5: Remove SSE parser**

Delete `cmd/serf-tui/sse_client.go` and `cmd/serf-tui/sse_client_test.go`. Remove all `sseEventMsg`, `sseConnectedMsg`, and `sseErrorMsg` references from TUI files.

- [ ] **Step 6: Run TUI tests**

Run:

```bash
go test ./cmd/serf-tui
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui internal/hubapi
git commit -m "feat(tui): use appwire protocol"
```

---

## Task 10: Move Browser Session State To JSON-RPC Websocket

**Files:**
- Modify: `cmd/serf-hub/templates/app.html`
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/assets/spawn.js`
- Modify: `cmd/serf-hub/assets/search.js`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Create: `cmd/serf-hub/assets/rpc.js`
- Create: `cmd/serf-hub/assets/session_store.js`
- Modify: `cmd/serf-hub/jstest/test-realistic-flow.js`
- Modify: `cmd/serf-hub/jstest/test-renderer.js`

- [ ] **Step 1: Add browser RPC unit tests**

Create JS tests that instantiate `RpcClient` with a fake websocket and verify:

- `initialize` is sent on open.
- Request IDs increment.
- Responses resolve the matching request.
- Notifications are passed to the store.

- [ ] **Step 2: Add `rpc.js`**

Create `cmd/serf-hub/assets/rpc.js` with:

```js
export class RpcClient {
  constructor(url) {
    this.url = url;
    this.nextId = 1;
    this.pending = new Map();
    this.notificationHandlers = [];
  }

  connect(WebSocketCtor = WebSocket) {
    this.ws = new WebSocketCtor(this.url);
    this.ws.addEventListener("message", (event) => this.handleMessage(event.data));
    return new Promise((resolve, reject) => {
      this.ws.addEventListener("open", async () => {
        try {
          await this.request("initialize", {
            clientInfo: { name: "serf-web", version: "0.1.0" },
            capabilities: {},
          });
          resolve();
        } catch (err) {
          reject(err);
        }
      });
      this.ws.addEventListener("error", reject);
    });
  }

  request(method, params) {
    const id = this.nextId++;
    const payload = JSON.stringify({ jsonrpc: "2.0", id, method, params });
    this.ws.send(payload);
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
    });
  }

  onNotification(handler) {
    this.notificationHandlers.push(handler);
  }

  handleMessage(raw) {
    const msg = JSON.parse(raw);
    if (msg.id && this.pending.has(msg.id)) {
      const pending = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) pending.reject(new Error(msg.error.message));
      else pending.resolve(msg.result);
      return;
    }
    if (msg.method) {
      for (const handler of this.notificationHandlers) handler(msg.method, msg.params);
    }
  }
}
```

- [ ] **Step 3: Add session store**

Create `cmd/serf-hub/assets/session_store.js` to normalize threads, turns, items, and notifications. Include tests for assistant delta accumulation and item completion.

- [ ] **Step 4: Replace fetch calls**

Replace current fetches to `/api/search`, `/api/models`, `/api/spawn`, `/api/sessions/*`, and SSE event sources with RPC calls:

- `thread/list`
- `thread/read`
- `thread/start`
- `turn/start`
- `turn/steer`
- `turn/interrupt`
- `thread/clear`
- `thread/fork`
- `model/list`
- `serf/dirs/complete`

- [ ] **Step 5: Run JS tests**

Run:

```bash
cd cmd/serf-hub/jstest
npm test
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets cmd/serf-hub/templates cmd/serf-hub/jstest
git commit -m "feat(hub-web): use appwire websocket"
```

---

## Task 11: Remove REST/SSE And `internal/hubapi`

**Files:**
- Delete: `internal/hubapi/client.go`
- Delete: `internal/hubapi/client_test.go`
- Delete: `internal/hubapi/refs.go`
- Delete: `internal/hubapi/refs_test.go`
- Delete: `internal/hubapi/types.go`
- Delete: `cmd/serf-hub/proxy.go`
- Delete: `cmd/serf-hub/proxy_test.go`
- Delete old REST/SSE tests in `server/server_test.go` already replaced by Task 6
- Modify: `README.md`
- Modify: docs that mention `/status`, `/input`, `/events`, or `/api/sessions`

- [ ] **Step 1: Search for old imports and routes**

Run:

```bash
rg -n "internal/hubapi|/api/sessions|/api/tree|/api/spawn|/status|/input|/events|SSE|sseEventMsg|RESTProxy|SSEProxy" .
```

Expected: remaining matches are either in docs being updated in this task or in deleted files.

- [ ] **Step 2: Delete old packages and proxy files**

Run:

```bash
rm -r internal/hubapi
rm -f cmd/serf-hub/proxy.go cmd/serf-hub/proxy_test.go
```

- [ ] **Step 3: Update docs**

Update README and hub/TUI docs to state:

- Serf clients use JSON-RPC app-wire.
- Browser connects to `/rpc`.
- TUI connects to `/rpc`.
- `serf serve` registers app-wire rendezvous.
- REST/SSE endpoints are gone.

- [ ] **Step 4: Run full search again**

Run:

```bash
rg -n "internal/hubapi|/api/sessions|/api/tree|/api/spawn|/status|/input|/events|SSEProxy|RESTProxy" .
```

Expected: no runtime-code matches. Historical design docs may still match if they are clearly old dated specs.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
go test ./...
```

Expected: PASS.

Commit:

```bash
git add README.md docs cmd server internal rendezvous agent go.mod go.sum
git commit -m "refactor: remove legacy REST SSE backend"
```

---

## Task 12: End-To-End Verification

**Files:**
- Create: `cmd/serf-hub/appwire_e2e_test.go`
- Create: `cmd/serf-tui/appwire_e2e_test.go`
- Modify: existing e2e fixtures as needed

- [ ] **Step 1: Add hub e2e test**

Create `cmd/serf-hub/appwire_e2e_test.go` with a test that:

- Starts a hub on a random loopback port.
- Connects to `/rpc`.
- Sends `initialize`.
- Sends `thread/start` with an empty initial task.
- Sends `thread/list`.
- Asserts the created thread appears with a `local:` ref.

- [ ] **Step 2: Add turn e2e test**

Extend the e2e test to:

- Send `turn/start`.
- Read notifications until `turn/started`.
- Send `turn/interrupt`.
- Assert interrupt response succeeds.

- [ ] **Step 3: Add clear e2e test**

Add a test that:

- Starts a thread.
- Calls `thread/clear`.
- Asserts the returned ref differs from the original ref.
- Calls `thread/read` on the new ref.

- [ ] **Step 4: Add fork e2e test**

Add a test that:

- Starts a thread.
- Completes one user turn with a fake model.
- Calls Serf-extended `thread/fork` with `sourceTurnId`, `editedInput`, and `label`.
- Asserts the child thread has `forkedFromId` set to the parent thread ID.

- [ ] **Step 5: Run full verification**

Run:

```bash
go test ./...
cd cmd/serf-hub/jstest && npm test
```

Expected: PASS for both Go and JS tests.

- [ ] **Step 6: Build binaries**

Run:

```bash
go build ./cmd/serf
go build ./cmd/serf-hub
go build ./cmd/serf-tui
```

Expected: all builds complete with no output.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/appwire_e2e_test.go cmd/serf-tui/appwire_e2e_test.go
git commit -m "test: verify appwire backend end to end"
```

---

## Final Review Checklist

- [ ] `rg -n "internal/hubapi|SSEProxy|RESTProxy|sseEventMsg|/api/sessions|/status|/input|/events" cmd internal server agent rendezvous` has no runtime-code matches.
- [ ] `go test ./...` passes.
- [ ] `cd cmd/serf-hub/jstest && npm test` passes.
- [ ] `go build ./cmd/serf ./cmd/serf-hub ./cmd/serf-tui` passes.
- [ ] Starting `serf-hub` and connecting a browser uses `/rpc`.
- [ ] Starting `serf-tui` initializes app-wire and shows threads from `thread/list`.
- [ ] A live turn streams typed notifications without SSE.
- [ ] Clear returns a new source-qualified ref.
- [ ] Fork-from-turn works through Serf's `thread/fork` extension.
