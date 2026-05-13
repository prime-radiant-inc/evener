package appsource

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/rendezvous"
)

type LocalDaemonSource struct {
	sourceID string
	entries  func() []LocalDaemonEntry
	client   *http.Client
}

type LocalDaemonEntry struct {
	Entry     rendezvous.Entry
	SessionID string
	Status    string
}

func NewLocalDaemonSource(sourceID string, entries func() []rendezvous.Entry, client *http.Client) *LocalDaemonSource {
	return NewLocalDaemonSourceWithEntries(sourceID, func() []LocalDaemonEntry {
		if entries == nil {
			return nil
		}
		raw := entries()
		out := make([]LocalDaemonEntry, 0, len(raw))
		for _, entry := range raw {
			out = append(out, LocalDaemonEntry{Entry: entry})
		}
		return out
	}, client)
}

func NewLocalDaemonSourceWithEntries(sourceID string, entries func() []LocalDaemonEntry, client *http.Client) *LocalDaemonSource {
	if sourceID == "" {
		sourceID = "local"
	}
	return &LocalDaemonSource{sourceID: sourceID, entries: entries, client: client}
}

func (s *LocalDaemonSource) ID() string {
	return s.sourceID
}

func (s *LocalDaemonSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	out := appwire.ThreadListResponse{}
	for _, entry := range s.liveEntries() {
		out.Data = append(out.Data, s.threadFromEntry(entry))
	}
	sort.SliceStable(out.Data, func(i, j int) bool {
		return localThreadLess(out.Data[i], out.Data[j])
	})
	return out, nil
}

func (s *LocalDaemonSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	entry, err := s.entryForRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	var out appwire.ThreadReadResponse
	err = s.withClient(ctx, entry, func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadRead(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.Unavailable("local daemon source cannot start threads directly")
}

func (s *LocalDaemonSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("local daemon source cannot resume threads directly")
}

func (s *LocalDaemonSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, appwire.Unavailable("local daemon source cannot fork threads directly")
}

func (s *LocalDaemonSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	var out appwire.TurnStartResponse
	err = s.withClient(ctx, entry, func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.TurnStart(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) SteerTurn(ctx context.Context, params appwire.TurnSteerParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.TurnSteer(ctx, params)
	})
}

func (s *LocalDaemonSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.TurnInterrupt(ctx, params)
	})
}

func (s *LocalDaemonSource) CompactThread(ctx context.Context, params appwire.ThreadCompactStartParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.ThreadCompactStart(ctx, params)
	})
}

func (s *LocalDaemonSource) ShutdownThread(ctx context.Context, params appwire.ThreadShutdownParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.ThreadShutdown(ctx, params)
	})
}

func (s *LocalDaemonSource) SetThreadModel(ctx context.Context, params appwire.ThreadModelSetParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.ThreadModelSet(ctx, params)
	})
}

func (s *LocalDaemonSource) ClearThread(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.ThreadClearResponse{}, err
	}
	var out appwire.ThreadClearResponse
	err = s.withClient(ctx, entry, func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadClear(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) ListModels(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	entries := s.liveEntries()
	if len(entries) == 0 {
		return appwire.ModelListResponse{}, nil
	}
	var out appwire.ModelListResponse
	err := s.withClient(ctx, localDaemonRendezvousEntry(entries[0]), func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.ModelList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) ListTasks(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TaskListResponse{}, err
	}
	var out appwire.TaskListResponse
	err = s.withClient(ctx, entry, func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.TasksList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	entry, err := s.entryForRef(params.Ref, params.ThreadID)
	if err != nil {
		return nil, err
	}
	transport, err := appwire.DialWebSocketWithHeaders(ctx, entry.Endpoint, s.client, daemonAuthHeader(entry.HubToken))
	if err != nil {
		return nil, err
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serf-hub"}}); err != nil {
		transport.Close()
		return nil, err
	}
	if _, err := client.ThreadRead(ctx, params); err != nil {
		transport.Close()
		return nil, err
	}
	out := make(chan appwire.Notification, 128)
	go func() {
		defer close(out)
		defer transport.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case notification, ok := <-client.Notifications():
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case out <- notification:
				default:
				}
			}
		}
	}()
	return out, nil
}

func (s *LocalDaemonSource) withClient(ctx context.Context, entry rendezvous.Entry, fn func(*appwire.Client) error) error {
	transport, err := appwire.DialWebSocketWithHeaders(ctx, entry.Endpoint, s.client, daemonAuthHeader(entry.HubToken))
	if err != nil {
		return err
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serf-hub"}}); err != nil {
		return err
	}
	return fn(client)
}

func daemonAuthHeader(token string) http.Header {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	return header
}

func (s *LocalDaemonSource) entryForRef(rawRef, threadID string) (rendezvous.Entry, error) {
	if rawRef != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err != nil {
			return rendezvous.Entry{}, err
		}
		if ref.SourceID != s.sourceID {
			return rendezvous.Entry{}, fmt.Errorf("source not found: %s", ref.SourceID)
		}
		threadID = ref.ThreadID
	}
	for _, item := range s.liveEntries() {
		entry := localDaemonRendezvousEntry(item)
		if localDaemonThreadID(item) == threadID || entry.SessionID == threadID {
			return entry, nil
		}
	}
	return rendezvous.Entry{}, fmt.Errorf("thread not found: %s", threadID)
}

func (s *LocalDaemonSource) liveEntries() []LocalDaemonEntry {
	if s.entries == nil {
		return nil
	}
	entries := s.entries()
	out := make([]LocalDaemonEntry, 0, len(entries))
	for _, item := range entries {
		entry := item.Entry
		if entry.Protocol != appwire.ProtocolVersion || entry.Endpoint == "" || entry.ThreadID == "" {
			continue
		}
		sourceID := entry.SourceID
		if sourceID == "" {
			sourceID = s.sourceID
		}
		if sourceID != s.sourceID {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *LocalDaemonSource) threadFromEntry(item LocalDaemonEntry) appwire.Thread {
	entry := localDaemonRendezvousEntry(item)
	threadID := localDaemonThreadID(item)
	ref := appwire.Ref{SourceID: s.sourceID, ThreadID: threadID}.String()
	startedAt := int64(0)
	if !entry.StartedAt.IsZero() {
		startedAt = entry.StartedAt.Unix()
	}
	return appwire.Thread{
		ID:            threadID,
		SessionID:     entry.SessionID,
		Preview:       entry.SessionID,
		ModelProvider: firstLocalDaemonValue(entry.Model, entry.Provider),
		CreatedAt:     startedAt,
		UpdatedAt:     startedAt,
		CWD:           entry.WorkingDir,
		Path:          filepath.Base(entry.WorkingDir),
		Source:        s.sourceID,
		Serf: appwire.SerfThread{
			Ref: ref,
			Capabilities: appwire.ThreadCapabilities{
				Send:         true,
				Steer:        true,
				Interrupt:    true,
				Compact:      true,
				Clear:        true,
				ForkFromTurn: true,
				Shutdown:     true,
				ChangeModel:  true,
			},
		},
		Status: appwire.ThreadStatus{Type: localDaemonThreadStatus(item.Status)},
	}
}

func localDaemonRendezvousEntry(item LocalDaemonEntry) rendezvous.Entry {
	entry := item.Entry
	if item.SessionID != "" {
		entry.SessionID = item.SessionID
	}
	return entry
}

func localDaemonThreadID(item LocalDaemonEntry) string {
	if item.SessionID != "" {
		return item.SessionID
	}
	return item.Entry.ThreadID
}

func localDaemonThreadStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PROCESSING", "STREAMING", "TOOL", "COMPACTING":
		return appwire.ThreadStatusProcessing
	case "AWAITING", "AWAITING_INPUT", "AWAITING_REPLY":
		return appwire.ThreadStatusAwaiting
	case "WARNING":
		return appwire.ThreadStatusWarning
	case "ERRORED", "ERROR":
		return appwire.ThreadStatusError
	case "CLOSED":
		return appwire.ThreadStatusClosed
	case "ENDED":
		return appwire.ThreadStatusEnded
	default:
		return appwire.ThreadStatusIdle
	}
}

func firstLocalDaemonValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func localThreadLess(a, b appwire.Thread) bool {
	au, bu := localThreadUpdatedAt(a), localThreadUpdatedAt(b)
	if au != bu {
		return au > bu
	}
	ac, bc := localThreadCreatedAt(a), localThreadCreatedAt(b)
	if ac != bc {
		return ac > bc
	}
	if cmp := compareLocalOrderText(localThreadTitle(a), localThreadTitle(b)); cmp != 0 {
		return cmp < 0
	}
	return compareLocalOrderText(firstLocalNonEmpty(a.ID, a.SessionID), firstLocalNonEmpty(b.ID, b.SessionID)) < 0
}

func localThreadUpdatedAt(thread appwire.Thread) int64 {
	if thread.UpdatedAt > 0 {
		return thread.UpdatedAt
	}
	if thread.CreatedAt > 0 {
		return thread.CreatedAt
	}
	return 0
}

func localThreadCreatedAt(thread appwire.Thread) int64 {
	if thread.CreatedAt > 0 {
		return thread.CreatedAt
	}
	if thread.UpdatedAt > 0 {
		return thread.UpdatedAt
	}
	return 0
}

func localThreadTitle(thread appwire.Thread) string {
	return firstLocalNonEmpty(thread.Name, thread.Preview, thread.SessionID, thread.ID)
}

func compareLocalOrderText(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	af := strings.ToLower(a)
	bf := strings.ToLower(b)
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func firstLocalNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
