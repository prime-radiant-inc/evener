package appwire

import (
	"context"
	"errors"
)

// ThreadHistoryRead uses serving-fact reads where supported. An older peer's
// typed method-not-found retires both new read methods for this connection.
func (c *Client) ThreadHistoryRead(ctx context.Context, params ThreadReadParams) (ThreadHistoryReadResponse, error) {
	var out ThreadHistoryReadResponse
	if handled, err := c.requestHistory(ctx, MethodThreadHistoryRead, params, &out); handled || err != nil {
		return out, err
	}
	err := c.request(ctx, MethodThreadRead, params, &out.ThreadReadResponse)
	out.HistoryServingFacts = HistoryServingFacts{ServingFactsUnavailableReason: "older-source"}
	return out, err
}

func (c *Client) ThreadHistoryTurnsList(ctx context.Context, params ThreadTurnsListParams) (ThreadHistoryTurnsListResponse, error) {
	var out ThreadHistoryTurnsListResponse
	if handled, err := c.requestHistory(ctx, MethodThreadHistoryTurnsList, params, &out); handled || err != nil {
		return out, err
	}
	err := c.request(ctx, MethodThreadTurnsList, params, &out.ThreadTurnsListResponse)
	out.HistoryServingFacts = HistoryServingFacts{ServingFactsUnavailableReason: "older-source"}
	return out, err
}

// requestHistory caches one transition to legacy mode, not one in-flight probe.
// Each concurrent request falls back for itself on typed method-not-found.
// False with no error asks the caller to use its unchanged legacy branch.
func (c *Client) requestHistory(ctx context.Context, method string, params, out any) (bool, error) {
	if c.historyUnsupported.Load() {
		return false, nil
	}
	err := c.request(ctx, method, params, out)
	if historyMethodMissing(err) {
		c.historyUnsupported.Store(true)
		return false, nil
	}
	return true, err
}

func historyMethodMissing(err error) bool {
	var wire WireError
	return errors.As(err, &wire) && wire.Code == CodeMethodNotFound
}
