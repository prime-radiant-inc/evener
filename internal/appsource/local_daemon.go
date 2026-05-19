package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"nhooyr.io/websocket"
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

func (s *LocalDaemonSource) QueueTurn(ctx context.Context, params appwire.TurnQueueParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.TurnQueue(ctx, params)
	})
}

func (s *LocalDaemonSource) DrainAsSteer(ctx context.Context, params appwire.TurnDrainAsSteerParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.TurnDrainAsSteer(ctx, params)
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
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, localDaemonDialError(err)
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serf-hub"}}); err != nil {
		transport.Close()
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, localDaemonInitializeError(err)
	}
	readParams := params
	readParams.Subscribe = true
	if _, err := client.ThreadRead(ctx, readParams); err != nil {
		transport.Close()
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, localDaemonSubscribeReadError(err)
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
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return localDaemonDialError(err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serf-hub"}}); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return localDaemonInitializeError(err)
	}
	if err := fn(client); err != nil {
		return localDaemonCallError(err)
	}
	return nil
}

// localDaemonDialError maps transport-level failures observed during the
// websocket dial or Initialize handshake to a SessionUnavailable wire error,
// so the hub's auto-resume gate can fire. It does NOT map application-level
// JSON-RPC error responses (which retain semantic meaning) or caller context
// cancellation (which the call site checks separately).
func localDaemonDialError(err error) error {
	if err == nil {
		return nil
	}

	// syscall-level signals that the daemon process is gone or the kernel
	// tore down the socket mid-handshake.
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Daemon accepted the connection then immediately closed it (process died
	// mid-handshake, or the websocket library surfaced an EOF during the
	// Initialize round trip).
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Transport-level timeouts: hung daemon, slow loopback, or network filter
	// holding the SYN. These manifest as net.Error.Timeout()==true (e.g.
	// kernel TCP retransmit timeout, *net.OpError with i/o timeout) or as
	// context.DeadlineExceeded from a child context inside the dial library
	// (the caller's ctx was already checked at the call site).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Websocket close error during the handshake: the daemon answered the
	// HTTP upgrade but the connection died before Initialize completed.
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Fallback string match for transport-shaped errors that the underlying
	// library wraps without exposing a typed sentinel (e.g. "use of closed
	// network connection", "connection reset by peer", "broken pipe").
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "use of closed network connection") ||
		strings.Contains(lower, "i/o timeout") {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	return err
}

func localDaemonCallError(err error) error {
	var wire appwire.WireError
	if errors.As(err, &wire) && wire.Code != appwire.CodeInternalError {
		return err
	}
	if !errors.As(err, &wire) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return localDaemonDialError(err)
	}
	msg := strings.ToLower(wire.Message)
	if strings.Contains(msg, "failed to get reader") ||
		strings.Contains(msg, "websocket") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "i/o timeout") {
		return appwire.SessionUnavailable("local daemon unavailable: " + wire.Message)
	}
	return err
}

func localDaemonInitializeError(err error) error {
	mapped := localDaemonCallError(err)
	var wire appwire.WireError
	if errors.As(mapped, &wire) && wire.Code != appwire.CodeInternalError {
		return mapped
	}
	return localDaemonDialError(mapped)
}

func localDaemonSubscribeReadError(err error) error {
	mapped := localDaemonCallError(err)
	var wire appwire.WireError
	if errors.As(mapped, &wire) && wire.Code != appwire.CodeInternalError {
		return mapped
	}
	return localDaemonDialError(mapped)
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
	return rendezvous.Entry{}, appwire.SessionUnavailable("thread not found: " + threadID)
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
	status := localDaemonThreadStatus(item.Status)
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
				Queue:        status == appwire.ThreadStatusProcessing,
			},
		},
		Status: appwire.ThreadStatus{Type: status},
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
