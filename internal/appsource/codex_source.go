package appsource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"primeradiant.com/serf/internal/appwire"
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

type codexLiveThread struct {
	client *appwire.Client
	close  func() error
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
			Statuses:         params.Statuses,
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
		return appwire.ThreadStartResponse{}, err
	}
	thread := s.mapThread(out.Thread)
	live := &codexLiveThread{client: client, close: closeClient}
	s.setLiveThread(thread.ID, live)
	resp := appwire.ThreadStartResponse{Thread: thread}
	if params.Prompt != "" || len(params.Items) > 0 {
		turnResp, err := s.StartTurn(ctx, appwire.TurnStartParams{Ref: thread.Serf.Ref, Prompt: params.Prompt, Items: params.Items})
		if err != nil {
			s.removeLiveThread(thread.ID, live)
			_ = live.close()
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
		return client.Request(ctx, appwire.MethodThreadResume, map[string]any{"threadId": threadID}, &out)
	})
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	return appwire.ThreadResumeResponse{Thread: s.mapThread(out.Thread)}, nil
}

func (s *CodexSource) ForkThread(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	threadID, err := s.threadID(params.Ref, "")
	if err != nil {
		return appwire.ThreadForkResponse{}, err
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
	return appwire.ThreadForkResponse{Thread: s.mapThread(out.Thread)}, nil
}

func (s *CodexSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	threadID, err := s.threadID(params.Ref, "")
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	input, err := codexInput(params.Prompt, params.Items)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	var out codexTurnStartResponse
	err = s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnStart, map[string]any{
			"threadId": threadID,
			"input":    input,
		}, &out)
	})
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	return appwire.TurnStartResponse{Turn: mapCodexTurn(out.Turn)}, nil
}

func (s *CodexSource) SteerTurn(ctx context.Context, params appwire.TurnSteerParams) error {
	threadID, err := s.threadID(params.Ref, "")
	if err != nil {
		return err
	}
	if params.TurnID == "" {
		return appwire.InvalidParams("turnId is required for codex turn/steer")
	}
	input, err := codexInput(params.Text, nil)
	if err != nil {
		return err
	}
	return s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnSteer, map[string]any{
			"threadId":       threadID,
			"expectedTurnId": params.TurnID,
			"input":          input,
		}, nil)
	})
}

func (s *CodexSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) error {
	threadID, err := s.threadID(params.Ref, "")
	if err != nil {
		return err
	}
	if params.TurnID == "" {
		return appwire.InvalidParams("turnId is required for codex turn/interrupt")
	}
	return s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnInterrupt, map[string]any{"threadId": threadID, "turnId": params.TurnID}, nil)
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

func (s *CodexSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return appwire.Unavailable("codex source does not support thread/model/set")
}

func (s *CodexSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("codex source does not support thread/clear")
}

func (s *CodexSource) ListModels(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	var out codexModelListResponse
	err := s.withClient(ctx, func(client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodModelList, params, &out)
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
	if err := client.Request(ctx, appwire.MethodThreadResume, map[string]any{"threadId": threadID}, &resume); err != nil {
		_ = closeClient()
		return nil, err
	}
	live := &codexLiveThread{client: client, close: closeClient}
	s.setLiveThread(threadID, live)
	return s.subscribeLiveThread(ctx, threadID, live), nil
}

func (s *CodexSource) liveThread(threadID string) *codexLiveThread {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[threadID]
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
		_ = previous.close()
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
	out := make(chan appwire.Notification, 128)
	go func() {
		defer close(out)
		defer s.removeLiveThread(threadID, live)
		defer live.close()
		for {
			select {
			case <-ctx.Done():
				return
			case notification, ok := <-live.client.Notifications():
				if !ok {
					return
				}
				mapped := s.mapNotification(threadID, notification)
				select {
				case <-ctx.Done():
					return
				case out <- mapped:
				default:
				}
			}
		}
	}()
	return out
}

func (s *CodexSource) withClient(ctx context.Context, fn func(*appwire.Client) error) error {
	client, closeClient, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer closeClient()
	return fn(client)
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
		return nil, nil, err
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo:   appwire.ClientInfo{Name: "serf-hub", Version: "0.1.0"},
		Capabilities: appwire.Capabilities{ExperimentalAPI: true},
	}); err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	if err := client.Notify(ctx, "initialized", nil); err != nil {
		_ = transport.Close()
		return nil, nil, err
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
		ModelProvider: thread.ModelProvider,
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
			Ref: ref,
			Capabilities: appwire.ThreadCapabilities{
				Send: true,
			},
		},
	}
	for _, turn := range thread.Turns {
		out.Turns = append(out.Turns, mapCodexTurn(turn))
	}
	return out
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
			return notificationMessage(notification.Method, map[string]any{
				"threadId": firstNonEmpty(params.ThreadID, threadID),
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
			return notificationMessage(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": firstNonEmpty(params.ThreadID, threadID),
				"turn":     mapCodexTurn(params.Turn),
			})
		}
	}
	return notification
}

type codexThreadListParams struct {
	Cursor           string   `json:"cursor,omitempty"`
	Limit            int      `json:"limit,omitempty"`
	SortKey          string   `json:"sortKey,omitempty"`
	SortDirection    string   `json:"sortDirection,omitempty"`
	SearchTerm       string   `json:"searchTerm,omitempty"`
	Statuses         []string `json:"statuses,omitempty"`
	IncludeSubagents bool     `json:"includeSubagents,omitempty"`
}

type codexThreadListResponse struct {
	Data            []codexThread `json:"data"`
	NextCursor      string        `json:"nextCursor,omitempty"`
	BackwardsCursor string        `json:"backwardsCursor,omitempty"`
}

type codexThreadReadResponse struct {
	Thread codexThread `json:"thread"`
}

type codexThreadStartResponse struct {
	Thread codexThread `json:"thread"`
}

type codexThreadResumeResponse struct {
	Thread codexThread `json:"thread"`
}

type codexThreadForkResponse struct {
	Thread codexThread `json:"thread"`
}

type codexTurnStartResponse struct {
	Turn codexTurn `json:"turn"`
}

type codexThread struct {
	ID            string            `json:"id"`
	SessionID     string            `json:"sessionId"`
	ForkedFromID  string            `json:"forkedFromId,omitempty"`
	Preview       string            `json:"preview"`
	Ephemeral     bool              `json:"ephemeral"`
	ModelProvider string            `json:"modelProvider"`
	CreatedAt     int64             `json:"createdAt"`
	UpdatedAt     int64             `json:"updatedAt"`
	Status        codexThreadStatus `json:"status"`
	Path          string            `json:"path"`
	CWD           string            `json:"cwd"`
	CLIVersion    string            `json:"cliVersion"`
	Source        string            `json:"source"`
	ThreadSource  string            `json:"threadSource,omitempty"`
	AgentNickname string            `json:"agentNickname,omitempty"`
	AgentRole     string            `json:"agentRole,omitempty"`
	Name          string            `json:"name,omitempty"`
	Turns         []codexTurn       `json:"turns,omitempty"`
}

type codexThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

type codexTurn struct {
	ID          string            `json:"id"`
	Items       []json.RawMessage `json:"items,omitempty"`
	ItemsView   string            `json:"itemsView"`
	Status      string            `json:"status"`
	Error       *codexTurnError   `json:"error,omitempty"`
	StartedAt   *int64            `json:"startedAt,omitempty"`
	CompletedAt *int64            `json:"completedAt,omitempty"`
	DurationMS  *int64            `json:"durationMs,omitempty"`
}

type codexTurnError struct {
	Message           string `json:"message"`
	AdditionalDetails string `json:"additionalDetails,omitempty"`
}

type codexModelListResponse struct {
	Data []struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	} `json:"data"`
}

func mapCodexThreadStatus(status codexThreadStatus) appwire.ThreadStatus {
	switch status.Type {
	case "active":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusProcessing, ActiveFlags: status.ActiveFlags}
	case "idle":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}
	case "systemError":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusError}
	case "notLoaded", "":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusEnded}
	default:
		return appwire.ThreadStatus{Type: status.Type, ActiveFlags: status.ActiveFlags}
	}
}

func mapCodexTurn(turn codexTurn) appwire.Turn {
	out := appwire.Turn{
		ID:          turn.ID,
		ItemsView:   firstNonEmpty(turn.ItemsView, "full"),
		Status:      mapCodexTurnStatus(turn.Status),
		StartedAt:   turn.StartedAt,
		CompletedAt: turn.CompletedAt,
		DurationMS:  turn.DurationMS,
	}
	if turn.Error != nil {
		out.Error = &appwire.TurnError{Message: firstNonEmpty(turn.Error.Message, turn.Error.AdditionalDetails), Source: "codex"}
	}
	for _, item := range turn.Items {
		out.Items = append(out.Items, mapCodexItem(turn.ID, item))
	}
	return out
}

func mapCodexTurnStatus(status string) string {
	switch status {
	case "inProgress":
		return appwire.TurnStatusRunning
	case "interrupted":
		return appwire.TurnStatusCanceled
	case "":
		return appwire.TurnStatusCompleted
	default:
		return status
	}
}

func mapCodexItem(turnID string, raw json.RawMessage) appwire.ThreadItem {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return appwire.ThreadItem{Type: "tool_call", TurnID: turnID, Raw: raw, Error: err.Error()}
	}
	itemType := rawString(obj["type"])
	item := appwire.ThreadItem{Type: itemType, ID: rawString(obj["id"]), TurnID: turnID, Raw: raw}
	switch itemType {
	case "userMessage":
		item.Type = "user_message"
		item.Text = codexInputText(obj["content"])
	case "agentMessage":
		item.Type = "agent_message"
		item.Text = rawString(obj["text"])
	case "commandExecution":
		item.Type = "tool_call"
		item.ToolName = "shell"
		item.CallID = item.ID
		item.ArgumentsJSON = jsonString(map[string]any{"command": rawString(obj["command"]), "cwd": rawString(obj["cwd"])})
		item.Output = rawString(obj["aggregatedOutput"])
		item.Status = rawString(obj["status"])
		if item.Status == "failed" {
			item.Error = "command failed"
		}
	case "mcpToolCall":
		item.Type = "tool_call"
		item.ToolName = rawString(obj["tool"])
		item.CallID = item.ID
		item.ArgumentsJSON = string(obj["arguments"])
		item.Status = rawString(obj["status"])
		item.Output = string(obj["result"])
		item.Error = string(obj["error"])
	case "dynamicToolCall":
		item.Type = "tool_call"
		item.ToolName = rawString(obj["tool"])
		item.CallID = item.ID
		item.ArgumentsJSON = string(obj["arguments"])
		item.Status = rawString(obj["status"])
		item.Output = string(obj["contentItems"])
	default:
		item.Type = "tool_call"
		item.ToolName = itemType
	}
	return item
}

func codexInput(prompt string, items []appwire.InputItem) ([]map[string]any, error) {
	var input []map[string]any
	if prompt != "" {
		input = append(input, map[string]any{"type": "text", "text": prompt})
	}
	for _, item := range items {
		switch item.Type {
		case "", "input_text", "text":
			if item.Text != "" {
				input = append(input, map[string]any{"type": "text", "text": item.Text})
			}
		case "input_image", "image":
			if url := strings.TrimSpace(item.URL); url != "" {
				input = append(input, map[string]any{"type": "image", "url": url})
				continue
			}
			if len(item.Data) == 0 {
				return nil, appwire.InvalidParams("codex image input requires image data")
			}
			mediaType := firstNonEmpty(item.MediaType, "image/png")
			input = append(input, map[string]any{
				"type": "image",
				"url":  "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(item.Data),
			})
		case "local_image", "localImage":
			path := firstNonEmpty(item.Path, item.Name)
			if path == "" {
				return nil, appwire.InvalidParams("codex localImage input requires path")
			}
			input = append(input, map[string]any{"type": "localImage", "path": path})
		case "skill":
			if item.Name == "" || item.Path == "" {
				return nil, appwire.InvalidParams("codex skill input requires name and path")
			}
			input = append(input, map[string]any{"type": "skill", "name": item.Name, "path": item.Path})
		case "mention":
			if item.Name == "" || item.Path == "" {
				return nil, appwire.InvalidParams("codex mention input requires name and path")
			}
			input = append(input, map[string]any{"type": "mention", "name": item.Name, "path": item.Path})
		default:
			return nil, appwire.InvalidParams("unsupported codex input item type: " + item.Type)
		}
	}
	return input, nil
}

func codexInputText(raw json.RawMessage) string {
	var inputs []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &inputs) != nil {
		return ""
	}
	var parts []string
	for _, input := range inputs {
		if input.Type == "text" && input.Text != "" {
			parts = append(parts, input.Text)
		}
	}
	return strings.Join(parts, "\n")
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
