package appwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var ErrNotificationOverflow = errors.New("appwire notification buffer overflow")

// notificationBufferCap sizes the Notifications() channel. Overflow is a
// deliberate loud failure (the connection is torn down rather than silently
// dropping or buffering without bound), so the capacity must hold any single
// legitimate burst even while the consumer waits for a scheduling slice: a
// codex initial-turn replay is ~160 messages, and request paths that never
// consume notifications (short-lived withClient calls) ride entirely on this
// buffer. 128 was smaller than a real burst and flaked under full-suite load
// (2026-06-12).
const notificationBufferCap = 4096

type Client struct {
	transport     Transport
	nextID        atomic.Int64
	sendMu        sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]pendingRequest
	notifications chan Notification
	pendingCoord  PendingCoordinator
	featuresMu    sync.RWMutex
	features      FeatureSet
}

type pendingRequest struct {
	id ID
	ch chan Message
}

func NewClient(transport Transport) *Client {
	c := &Client{
		transport:     transport,
		pending:       map[string]pendingRequest{},
		notifications: make(chan Notification, notificationBufferCap),
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
				if !c.enqueueNotification(*msg.Notification) {
					c.failPending(ErrNotificationOverflow)
					_ = c.transport.Close()
					close(c.notifications)
					return
				}
				continue
			}
			id := msg.IDString()
			c.pendingMu.Lock()
			pending := c.pending[id]
			delete(c.pending, id)
			c.pendingMu.Unlock()
			if pending.ch != nil {
				pending.ch <- msg
			}
		}
	}()
}

func (c *Client) enqueueNotification(notification Notification) bool {
	select {
	case c.notifications <- notification:
		return true
	default:
		return false
	}
}

func (c *Client) Notifications() <-chan Notification {
	return c.notifications
}

func (c *Client) Close() error {
	return c.transport.Close()
}

// SetPendingCoordinator installs an optimistic-rendering coordinator
// that observes the four conversation-affecting Turn* methods. Pass
// nil to disable. Safe to call before Start.
func (c *Client) SetPendingCoordinator(pc PendingCoordinator) {
	c.pendingCoord = pc
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	id := NewIntID(c.nextID.Add(1) - 1)
	ch := make(chan Message, 1)

	c.pendingMu.Lock()
	c.pending[id.String()] = pendingRequest{id: id, ch: ch}
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
		wire := msg.Error.Error
		wire.Message = fmt.Sprintf("appwire %s: %s", method, wire.Message)
		return wire
	}
	if msg.Response == nil {
		return fmt.Errorf("appwire %s: expected response", method)
	}
	if out == nil {
		return nil
	}
	data, err := json.Marshal(msg.Response.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *Client) Request(ctx context.Context, method string, params any, out any) error {
	return c.request(ctx, method, params, out)
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.transport.Send(ctx, NotificationMessage(method, params))
}

func (c *Client) removePending(id ID) {
	c.pendingMu.Lock()
	delete(c.pending, id.String())
	c.pendingMu.Unlock()
}

func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, pending := range c.pending {
		delete(c.pending, id)
		pending.ch <- ErrorMessage(pending.id, InternalError(err.Error()))
	}
}

func (c *Client) Initialize(ctx context.Context, params InitializeParams) (InitializeResponse, error) {
	var out InitializeResponse
	err := c.request(ctx, MethodInitialize, params, &out)
	if err == nil {
		c.featuresMu.Lock()
		c.features = out.Features
		c.featuresMu.Unlock()
	}
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

func (c *Client) ThreadTurnsList(ctx context.Context, params ThreadTurnsListParams) (ThreadTurnsListResponse, error) {
	var out ThreadTurnsListResponse
	err := c.request(ctx, MethodThreadTurnsList, params, &out)
	return out, err
}

func (c *Client) ThreadTurnItemsList(ctx context.Context, params ThreadTurnItemsListParams) (ThreadTurnItemsListResponse, error) {
	var out ThreadTurnItemsListResponse
	err := c.request(ctx, MethodThreadTurnItemsList, params, &out)
	return out, err
}

func (c *Client) ThreadTranscriptList(ctx context.Context, params ThreadTranscriptListParams) (ThreadTranscriptListResponse, error) {
	var out ThreadTranscriptListResponse
	err := c.request(ctx, MethodSerfThreadTranscriptsList, params, &out)
	return out, err
}

func (c *Client) ThreadStart(ctx context.Context, params ThreadStartParams) (ThreadStartResponse, error) {
	var out ThreadStartResponse
	err := c.request(ctx, MethodThreadStart, params, &out)
	return out, err
}

func (c *Client) ThreadResume(ctx context.Context, params ThreadResumeParams) (ThreadResumeResponse, error) {
	var out ThreadResumeResponse
	err := c.request(ctx, MethodThreadResume, params, &out)
	return out, err
}

func (c *Client) ThreadFork(ctx context.Context, params ThreadForkParams) (ThreadForkResponse, error) {
	var out ThreadForkResponse
	err := c.request(ctx, MethodThreadFork, params, &out)
	return out, err
}

func (c *Client) ThreadClear(ctx context.Context, params ThreadClearParams) (ThreadClearResponse, error) {
	var out ThreadClearResponse
	err := c.request(ctx, MethodThreadClear, params, &out)
	return out, err
}

func (c *Client) ThreadModelSet(ctx context.Context, params ThreadModelSetParams) error {
	return c.request(ctx, MethodThreadModelSet, params, nil)
}

func (c *Client) ThreadCompactStart(ctx context.Context, params ThreadCompactStartParams) error {
	return c.request(ctx, MethodThreadCompactStart, params, nil)
}

func (c *Client) ThreadShutdown(ctx context.Context, params ThreadShutdownParams) error {
	return c.request(ctx, MethodThreadShutdown, params, nil)
}

func (c *Client) TurnStart(ctx context.Context, params TurnStartParams) (TurnStartResponse, error) {
	var out TurnStartResponse
	err := c.request(ctx, MethodTurnStart, params, &out)
	return out, err
}

func (c *Client) TurnSteer(ctx context.Context, params TurnSteerParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnSteer, inputText(params.Input), pendingTargetRef(params.Ref, params.ThreadID))
	}
	err := c.request(ctx, MethodTurnSteer, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}

func inputText(input []InputItem) string {
	for _, item := range input {
		if strings.TrimSpace(item.Text) != "" {
			return item.Text
		}
	}
	return ""
}

func (c *Client) TurnInterrupt(ctx context.Context, params TurnInterruptParams) error {
	return c.request(ctx, MethodTurnInterrupt, params, nil)
}

// TurnQueue calls turn/queue (kata 111a) to enqueue a user message during a
// running turn. The daemon returns immediately once the text has been
// recorded; the queued message is processed as a fresh user turn after the
// active turn completes.
func (c *Client) TurnQueue(ctx context.Context, params TurnQueueParams) error {
	return c.request(ctx, MethodTurnQueue, params, nil)
}

// GoalSet calls goal/set to set (or, with an empty objective, clear) the
// session's /goal. The daemon returns immediately; GoalSetResponse.Started
// reports whether the goal loop began right away or after the current turn.
func (c *Client) GoalSet(ctx context.Context, params GoalSetParams) (GoalSetResponse, error) {
	var out GoalSetResponse
	err := c.request(ctx, MethodGoalSet, params, &out)
	return out, err
}

// TurnDrainAsSteer calls turn/drainAsSteer (kata 0bq1) to drain every queued
// message into a single STEERING message for the in-flight turn.
func (c *Client) TurnDrainAsSteer(ctx context.Context, params TurnDrainAsSteerParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnDrainAsSteer, "", params.Ref)
	}
	err := c.request(ctx, MethodTurnDrainAsSteer, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}

func pendingTargetRef(ref, threadID string) string {
	if strings.TrimSpace(ref) != "" {
		return ref
	}
	return threadID
}

func (c *Client) TasksList(ctx context.Context, params TaskListParams) (TaskListResponse, error) {
	var out TaskListResponse
	err := c.request(ctx, MethodSerfTasksList, params, &out)
	return out, err
}

func (c *Client) DirsComplete(ctx context.Context, params DirsCompleteParams) (DirsCompleteResponse, error) {
	var out DirsCompleteResponse
	err := c.request(ctx, MethodSerfDirsComplete, params, &out)
	return out, err
}

func (c *Client) HarnessList(ctx context.Context, params HarnessListParams) (HarnessListResponse, error) {
	var out HarnessListResponse
	err := c.request(ctx, MethodSerfHarnessesList, params, &out)
	return out, err
}

func (c *Client) AuthStatus(ctx context.Context, params AuthStatusParams) (AuthStatusResponse, error) {
	var out AuthStatusResponse
	err := c.request(ctx, MethodSerfAuthStatus, params, &out)
	return out, err
}

func (c *Client) AuthLoginStart(ctx context.Context, params AuthLoginStartParams) (AuthLoginStartResponse, error) {
	var out AuthLoginStartResponse
	err := c.request(ctx, MethodSerfAuthLoginStart, params, &out)
	return out, err
}

func (c *Client) AuthLoginComplete(ctx context.Context, params AuthLoginCompleteParams) (AuthLoginCompleteResponse, error) {
	var out AuthLoginCompleteResponse
	err := c.request(ctx, MethodSerfAuthLoginComplete, params, &out)
	return out, err
}

func (c *Client) AuthLogout(ctx context.Context, params AuthLogoutParams) (AuthLogoutResponse, error) {
	var out AuthLogoutResponse
	err := c.request(ctx, MethodSerfAuthLogout, params, &out)
	return out, err
}

func (c *Client) ModelList(ctx context.Context, params ModelListParams) (ModelListResponse, error) {
	var out ModelListResponse
	err := c.request(ctx, MethodModelList, params, &out)
	return out, err
}
