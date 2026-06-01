package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"

	"github.com/coder/websocket"
	"primeradiant.com/serf/appwire"
)

type CodexSourceConfig struct {
	ID              string `toml:"id"`
	Endpoint        string `toml:"endpoint"`
	BearerToken     string `toml:"bearer_token"`
	BearerTokenFile string `toml:"bearer_token_file"`
}

type CodexSource struct {
	sourceID        string
	endpoint        string
	bearerToken     string
	bearerTokenFile string
	client          *http.Client
	mu              sync.Mutex
	live            map[string]*codexLiveThread
}

func NewCodexSource(cfg CodexSourceConfig, client *http.Client) *CodexSource {
	sourceID := cfg.ID
	if sourceID == "" {
		sourceID = "codex"
	}
	return &CodexSource{
		sourceID:        sourceID,
		endpoint:        cfg.Endpoint,
		bearerToken:     cfg.BearerToken,
		bearerTokenFile: cfg.BearerTokenFile,
		client:          client,
		live:            map[string]*codexLiveThread{},
	}
}

func (s *CodexSource) ID() string {
	return s.sourceID
}

func (s *CodexSource) RelayOnThreadRead() bool {
	return false
}

func (s *CodexSource) ListThreads(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	var out codexThreadListResponse
	err := s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodThreadList, codexThreadListParams{
			Cursor:           params.Cursor,
			Limit:            params.Limit,
			SortKey:          params.SortKey,
			SortDirection:    params.SortDirection,
			SearchTerm:       params.SearchTerm,
			Statuses:         codexThreadListStatuses(params.Statuses),
			IncludeSubagents: params.IncludeSubagents,
		}, &out)
	})
	if err != nil {
		return appwire.ThreadListResponse{}, err
	}
	resp := appwire.ThreadListResponse{NextCursor: out.NextCursor, BackwardsCursor: out.BackwardsCursor}
	for _, thread := range out.Data {
		resp.Data = append(resp.Data, s.mapThread(thread))
	}
	return resp, nil
}

func (s *CodexSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	out, err := s.readThread(ctx, threadID, params.IncludeTurns, params.ItemsView)
	if err != nil && params.IncludeTurns && codexTurnsUnavailableBeforeFirstMessage(err) {
		out, err = s.readThread(ctx, threadID, false, params.ItemsView)
	}
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return appwire.ThreadReadResponse{Thread: s.mapThread(out.Thread)}, nil
}

func (s *CodexSource) readThread(ctx context.Context, threadID string, includeTurns bool, itemsView string) (codexThreadReadResponse, error) {
	var out codexThreadReadResponse
	err := s.withClient(ctx, func(client *appwire.Client) error {
		req := map[string]any{
			"threadId":     threadID,
			"includeTurns": includeTurns,
		}
		if itemsView != "" {
			req["itemsView"] = itemsView
		}
		return client.Request(ctx, appwire.MethodThreadRead, req, &out)
	})
	if err != nil {
		return codexThreadReadResponse{}, err
	}
	return out, nil
}

func codexTurnsUnavailableBeforeFirstMessage(err error) bool {
	return strings.Contains(err.Error(), "includeTurns is unavailable before first user message")
}

func codexNoRolloutFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no rollout found for thread id")
}

func (s *CodexSource) StartThread(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	client, closeClient, err := s.connect(ctx)
	if err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	var out codexThreadStartResponse
	if err := client.Request(ctx, appwire.MethodThreadStart, map[string]any{
		"cwd":           emptyNil(params.CWD),
		"model":         emptyNil(params.Model),
		"modelProvider": emptyNil(params.ModelProvider),
		"threadSource":  "user",
	}, &out); err != nil {
		_ = closeClient()
		return appwire.ThreadStartResponse{}, codexSourceCallError(err)
	}
	thread := s.mapLifecycleThread(out.Thread, out.Model, out.ModelProvider)
	live := s.newLiveThread(thread.ID, client, closeClient)
	s.setLiveThread(thread.ID, live)
	resp := appwire.ThreadStartResponse{Thread: thread}
	if len(params.Input) > 0 {
		turnResp, err := s.startTurnWithClient(ctx, client, appwire.TurnStartParams{Ref: thread.Serf.Ref, Input: params.Input})
		if err != nil {
			s.removeLiveThread(thread.ID, live)
			live.retire()
			return appwire.ThreadStartResponse{}, err
		}
		resp.Turn = turnResp.Turn
	}
	return resp, nil
}

func (s *CodexSource) ResumeThread(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	threadID, err := s.threadID(params.Ref, params.Session)
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	var out codexThreadResumeResponse
	err = s.withClient(ctx, func(client *appwire.Client) error {
		return codexThreadResume(ctx, client, threadID, &out)
	})
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	return appwire.ThreadResumeResponse{Thread: s.mapLifecycleThread(out.Thread, out.Model, out.ModelProvider)}, nil
}

func (s *CodexSource) ForkThread(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	threadID, err := s.threadID(params.Ref, "")
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if codexForkHasEditAtTurnMetadata(params) {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("edit-at-turn fork is not available for codex sessions")
	}
	var out codexThreadForkResponse
	err = s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodThreadFork, map[string]any{
			"threadId":      threadID,
			"model":         emptyNil(params.Model),
			"modelProvider": emptyNil(params.ModelProvider),
		}, &out)
	})
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	return appwire.ThreadForkResponse{Thread: s.mapLifecycleThread(out.Thread, out.Model, out.ModelProvider)}, nil
}

func (s *CodexSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	input, err := codexInput("", params.Input)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	var out codexTurnStartResponse
	if live := s.liveThread(threadID); live != nil {
		if err := codexTurnStart(ctx, live.client, threadID, input, &out); err != nil {
			mapped := codexSourceCallError(err)
			if codexSourceSessionUnavailable(mapped) {
				s.removeLiveThread(threadID, live)
				live.retire()
			}
			return appwire.TurnStartResponse{}, mapped
		}
		return appwire.TurnStartResponse{Turn: mapCodexTurn(out.Turn)}, nil
	}
	client, closeClient, err := s.connect(ctx)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	var resume codexThreadResumeResponse
	if err := codexThreadResume(ctx, client, threadID, &resume); err != nil {
		_ = closeClient()
		return appwire.TurnStartResponse{}, codexSourceCallError(err)
	}
	live := s.newLiveThread(threadID, client, closeClient)
	s.setLiveThread(threadID, live)
	if err := codexTurnStart(ctx, client, threadID, input, &out); err != nil {
		s.removeLiveThread(threadID, live)
		live.retire()
		return appwire.TurnStartResponse{}, codexSourceCallError(err)
	}
	return appwire.TurnStartResponse{Turn: mapCodexTurn(out.Turn)}, nil
}

func (s *CodexSource) startTurnWithClient(ctx context.Context, client *appwire.Client, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	input, err := codexInput("", params.Input)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	var out codexTurnStartResponse
	if err := codexTurnStart(ctx, client, threadID, input, &out); err != nil {
		return appwire.TurnStartResponse{}, codexSourceCallError(err)
	}
	return appwire.TurnStartResponse{Turn: mapCodexTurn(out.Turn)}, nil
}

func codexTurnStart(ctx context.Context, client *appwire.Client, threadID string, input []map[string]any, out *codexTurnStartResponse) error {
	return client.Request(ctx, appwire.MethodTurnStart, map[string]any{
		"threadId": threadID,
		"input":    input,
	}, out)
}

func (s *CodexSource) SteerTurn(ctx context.Context, params appwire.TurnSteerParams) error {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return err
	}
	turnID := params.ExpectedTurnID
	if turnID == "" {
		return appwire.InvalidParams("expectedTurnId is required for codex turn/steer")
	}
	input, err := codexInput("", params.Input)
	if err != nil {
		return err
	}
	return s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnSteer, map[string]any{
			"threadId":       threadID,
			"expectedTurnId": turnID,
			"input":          input,
		}, nil)
	})
}

func (s *CodexSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) error {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return err
	}
	turnID := params.ExpectedTurnID
	if turnID == "" {
		return appwire.InvalidParams("expectedTurnId is required for codex turn/interrupt")
	}
	return s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnInterrupt, map[string]any{"threadId": threadID, "turnId": turnID, "expectedTurnId": turnID}, nil)
	})
}

func (s *CodexSource) CompactThread(ctx context.Context, params appwire.ThreadCompactStartParams) error {
	threadID, err := s.threadID(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodThreadCompactStart, map[string]any{"threadId": threadID}, nil)
	})
}

func (s *CodexSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return appwire.Unavailable("codex source does not support thread/shutdown")
}

func (s *CodexSource) QueueTurn(context.Context, appwire.TurnQueueParams) error {
	return appwire.Unavailable("codex source does not support turn/queue")
}

func (s *CodexSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return appwire.Unavailable("codex source does not support turn/drainAsSteer")
}

func (s *CodexSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return appwire.Unavailable("codex source does not support thread/model/set")
}

func (s *CodexSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("codex source does not support thread/clear")
}

func (s *CodexSource) ListModels(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	var out codexModelListResponse
	err := s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodModelList, codexModelListParams{}, &out)
	})
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	resp := appwire.ModelListResponse{}
	for _, model := range out.Data {
		resp.Data = append(resp.Data, appwire.ModelDescriptor{Provider: s.sourceID, Model: firstNonEmpty(model.Model, model.ID)})
	}
	return resp, nil
}

func (s *CodexSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, appwire.Unavailable("codex source does not expose serf tasks")
}

func (s *CodexSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return nil, err
	}
	if live := s.liveThread(threadID); live != nil {
		return s.subscribeLiveThread(ctx, threadID, live), nil
	}
	client, closeClient, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	var resume codexThreadResumeResponse
	if err := codexThreadResume(ctx, client, threadID, &resume); err != nil {
		_ = closeClient()
		if codexNoRolloutFound(err) {
			// Codex has no live rollout to subscribe to, but thread/read can still render the idle transcript.
			notifications := make(chan appwire.Notification)
			close(notifications)
			return notifications, nil
		}
		return nil, codexSourceCallError(err)
	}
	live := s.newLiveThread(threadID, client, closeClient)
	s.setLiveThread(threadID, live)
	return s.subscribeLiveThread(ctx, threadID, live), nil
}

func codexThreadResume(ctx context.Context, client *appwire.Client, threadID string, out *codexThreadResumeResponse) error {
	return client.Request(ctx, appwire.MethodThreadResume, map[string]any{"threadId": threadID}, out)
}

func (s *CodexSource) liveThread(threadID string) *codexLiveThread {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.live[threadID]
	if live != nil && live.isClosed() {
		delete(s.live, threadID)
		return nil
	}
	return live
}

func (s *CodexSource) newLiveThread(threadID string, client *appwire.Client, closeClient func() error) *codexLiveThread {
	live := &codexLiveThread{
		client:      client,
		close:       closeClient,
		done:        make(chan struct{}),
		subscribers: map[chan appwire.Notification]struct{}{},
	}
	go s.runLiveThread(threadID, live)
	go live.retireIfNoSubscriber(codexLiveNoSubscriberTimeout)
	return live
}

func (s *CodexSource) setLiveThread(threadID string, live *codexLiveThread) {
	if threadID == "" {
		return
	}
	s.mu.Lock()
	previous := s.live[threadID]
	s.live[threadID] = live
	s.mu.Unlock()
	if previous != nil && previous != live {
		previous.retire()
	}
}

func (s *CodexSource) removeLiveThread(threadID string, live *codexLiveThread) {
	s.mu.Lock()
	if s.live[threadID] == live {
		delete(s.live, threadID)
	}
	s.mu.Unlock()
}

func (s *CodexSource) subscribeLiveThread(ctx context.Context, threadID string, live *codexLiveThread) <-chan appwire.Notification {
	return live.subscribe(ctx)
}

func (s *CodexSource) runLiveThread(threadID string, live *codexLiveThread) {
	defer s.removeLiveThread(threadID, live)
	defer live.retire()
	defer live.finish()
	for notification := range live.client.Notifications() {
		live.publish(s.mapNotification(threadID, notification))
	}
}

func (s *CodexSource) withClient(ctx context.Context, fn func(*appwire.Client) error) error {
	client, closeClient, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = closeClient() }()
	return codexSourceCallError(fn(client))
}

// codexSourceDialError maps transport-level failures observed during the
// websocket dial or Initialize handshake against the codex app-server to a
// SessionUnavailable wire error, so the hub's auto-resume gate can fire. It
// does NOT map application-level JSON-RPC error responses (which retain
// semantic meaning) or caller context cancellation.
func codexSourceDialError(err error) error {
	if err == nil {
		return nil
	}

	// syscall-level signals that the daemon process is gone or the kernel
	// tore down the socket mid-handshake.
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return appwire.SessionUnavailable("codex daemon unavailable: " + err.Error())
	}

	// Daemon accepted the connection then immediately closed it (process died
	// mid-handshake, or the websocket library surfaced an EOF during the
	// Initialize round trip).
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return appwire.SessionUnavailable("codex daemon unavailable: " + err.Error())
	}

	// Transport-level timeouts: hung daemon, slow loopback, or network filter
	// holding the SYN.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return appwire.SessionUnavailable("codex daemon unavailable: " + err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return appwire.SessionUnavailable("codex daemon unavailable: " + err.Error())
	}

	// Websocket close error during the handshake: the daemon answered the
	// HTTP upgrade but the connection died before Initialize completed.
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return appwire.SessionUnavailable("codex daemon unavailable: " + err.Error())
	}

	// Fallback string match for transport-shaped errors that the underlying
	// library wraps without exposing a typed sentinel.
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "use of closed network connection") ||
		strings.Contains(lower, "i/o timeout") {
		return appwire.SessionUnavailable("codex daemon unavailable: " + err.Error())
	}

	return err
}

// codexSourceCallError wraps in-flight RPC failures where the codex
// app-server's websocket transport dropped (process died, connection reset,
// websocket closed) in a SessionUnavailable wire error. The appwire client
// surfaces such failures as CodeInternalError wrapping the underlying
// transport message; application-level errors (invalidParams,
// methodNotFound, etc.) keep their codes and pass through unchanged.
func codexSourceCallError(err error) error {
	if err == nil {
		return nil
	}
	var wire appwire.WireError
	if errors.As(err, &wire) && wire.Code != appwire.CodeInternalError {
		return err
	}
	if !errors.As(err, &wire) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return codexSourceDialError(err)
	}
	msg := strings.ToLower(wire.Message)
	if strings.Contains(msg, "failed to get reader") ||
		strings.Contains(msg, "websocket") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "i/o timeout") {
		return appwire.SessionUnavailable("codex daemon unavailable: " + wire.Message)
	}
	return err
}

func codexSourceSessionUnavailable(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		return false
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return data.SerfErrorInfo == appwire.ErrorSessionUnavailable
	case map[string]any:
		return data["serfErrorInfo"] == string(appwire.ErrorSessionUnavailable)
	default:
		return false
	}
}

func (s *CodexSource) connect(ctx context.Context) (*appwire.Client, func() error, error) {
	if s.endpoint == "" {
		return nil, nil, appwire.InvalidParams("codex endpoint is required")
	}
	header, err := s.authHeader()
	if err != nil {
		return nil, nil, err
	}
	transport, err := appwire.DialWebSocketWithHeaders(ctx, s.endpoint, s.client, header)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, codexSourceDialError(err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo:   appwire.ClientInfo{Name: "serf-hub", Version: "0.1.0"},
		Capabilities: appwire.Capabilities{ExperimentalAPI: true},
	}); err != nil {
		_ = transport.Close()
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, codexSourceDialError(codexSourceCallError(err))
	}
	if err := client.Notify(ctx, "initialized", nil); err != nil {
		_ = transport.Close()
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, codexSourceDialError(err)
	}
	return client, transport.Close, nil
}

func (s *CodexSource) authHeader() (http.Header, error) {
	token := strings.TrimSpace(s.bearerToken)
	if token == "" && s.bearerTokenFile != "" {
		data, err := os.ReadFile(s.bearerTokenFile)
		if err != nil {
			return nil, fmt.Errorf("read codex bearer token file: %w", err)
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		return nil, nil
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	return header, nil
}

func (s *CodexSource) threadID(rawRef, fallback string) (string, error) {
	if rawRef != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err != nil {
			return "", err
		}
		if ref.SourceID != s.sourceID {
			return "", fmt.Errorf("source not found: %s", ref.SourceID)
		}
		return ref.ThreadID, nil
	}
	if fallback == "" {
		return "", appwire.InvalidParams("threadId or ref is required")
	}
	return fallback, nil
}

func (s *CodexSource) mapThread(thread codexThread) appwire.Thread {
	ref := appwire.Ref{SourceID: s.sourceID, ThreadID: thread.ID}.String()
	out := appwire.Thread{
		ID:            thread.ID,
		SessionID:     firstNonEmpty(thread.SessionID, thread.ID),
		ForkedFromID:  thread.ForkedFromID,
		Preview:       thread.Preview,
		Ephemeral:     thread.Ephemeral,
		CreatedAt:     thread.CreatedAt,
		UpdatedAt:     thread.UpdatedAt,
		Status:        mapCodexThreadStatus(thread.Status),
		Path:          thread.Path,
		CWD:           thread.CWD,
		CLIVersion:    thread.CLIVersion,
		Source:        s.sourceID,
		ThreadSource:  thread.ThreadSource,
		AgentNickname: thread.AgentNickname,
		AgentRole:     thread.AgentRole,
		Name:          thread.Name,
		Serf: appwire.SerfThread{
			Profile: thread.ModelProvider,
			Ref:     ref,
			Capabilities: appwire.ThreadCapabilities{
				Send:      true,
				Steer:     codexThreadSupportsTurnActions(thread.Status.Type),
				Interrupt: codexThreadSupportsTurnActions(thread.Status.Type),
				Compact:   true,
			},
		},
	}
	for _, turn := range thread.Turns {
		out.Turns = append(out.Turns, mapCodexTurn(turn))
	}
	return out
}

func (s *CodexSource) mapLifecycleThread(thread codexThread, model, modelProvider string) appwire.Thread {
	out := s.mapThread(thread)
	if model = strings.TrimSpace(model); model != "" {
		out.ModelProvider = model
	}
	if modelProvider = strings.TrimSpace(modelProvider); modelProvider != "" {
		out.Serf.Profile = modelProvider
	}
	return out
}

func codexThreadSupportsTurnActions(status string) bool {
	switch status {
	case "active", "idle":
		return true
	default:
		return false
	}
}

func codexThreadListStatuses(statuses []string) []string {
	if len(statuses) == 0 {
		return nil
	}
	out := make([]string, 0, len(statuses))
	for _, status := range statuses {
		switch strings.TrimSpace(status) {
		case "active":
			out = append(out, "active")
		case "notLoaded":
			out = append(out, "notLoaded")
		case "systemError":
			out = append(out, "systemError")
		default:
			out = append(out, status)
		}
	}
	return out
}

func codexForkHasEditAtTurnMetadata(params appwire.ThreadForkParams) bool {
	return strings.TrimSpace(params.SourceTurnID) != "" ||
		strings.TrimSpace(params.EditedInput) != "" ||
		strings.TrimSpace(params.Label) != ""
}

func (s *CodexSource) mapNotification(threadID string, notification appwire.Notification) appwire.Notification {
	switch notification.Method {
	case "item/commandExecution/outputDelta":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
			Delta    string `json:"delta"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			params.ThreadID = firstNonEmpty(params.ThreadID, threadID)
			return notificationMessage(appwire.NotifyToolOutputDelta, map[string]any{
				"threadId": params.ThreadID,
				"ref":      appwire.Ref{SourceID: s.sourceID, ThreadID: params.ThreadID}.String(),
				"turnId":   params.TurnID,
				"itemId":   params.ItemID,
				"delta":    params.Delta,
			})
		}
	case appwire.NotifyAgentMessageDelta:
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			params.ThreadID = firstNonEmpty(params.ThreadID, threadID)
			params.Ref = appwire.Ref{SourceID: s.sourceID, ThreadID: params.ThreadID}.String()
			return notificationMessage(appwire.NotifyAgentMessageDelta, params)
		}
	case appwire.NotifyThreadStatusChanged:
		var params struct {
			ThreadID string            `json:"threadId"`
			Status   codexThreadStatus `json:"status"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			params.ThreadID = firstNonEmpty(params.ThreadID, threadID)
			return notificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
				ThreadID: params.ThreadID,
				Ref:      appwire.Ref{SourceID: s.sourceID, ThreadID: params.ThreadID}.String(),
				Status:   mapCodexThreadStatus(params.Status),
			})
		}
	case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
		var params struct {
			ThreadID string          `json:"threadId"`
			TurnID   string          `json:"turnId"`
			Item     json.RawMessage `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			mappedThreadID := firstNonEmpty(params.ThreadID, threadID)
			return notificationMessage(notification.Method, map[string]any{
				"threadId": mappedThreadID,
				"ref":      appwire.Ref{SourceID: s.sourceID, ThreadID: mappedThreadID}.String(),
				"turnId":   params.TurnID,
				"item":     mapCodexItem(params.TurnID, params.Item),
			})
		}
	case appwire.NotifyTurnCompleted:
		var params struct {
			ThreadID string    `json:"threadId"`
			Turn     codexTurn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			mappedThreadID := firstNonEmpty(params.ThreadID, threadID)
			return notificationMessage(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": mappedThreadID,
				"ref":      appwire.Ref{SourceID: s.sourceID, ThreadID: mappedThreadID}.String(),
				"turn":     mapCodexTurn(params.Turn),
			})
		}
	}
	return notification
}

func notificationMessage(method string, params any) appwire.Notification {
	data, err := json.Marshal(params)
	if err != nil {
		data = []byte(`{}`)
	}
	return appwire.Notification{Method: method, Params: data}
}

func rawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func jsonString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func emptyNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
