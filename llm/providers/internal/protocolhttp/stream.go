package protocolhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
)

// StreamDecoder consumes the live SSE response in its own goroutine and owns
// closing resp.Body and s, completing attempt, and calling cancel when done
// (the contract of today's decodeStream functions).
type StreamDecoder func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *Result, attempt *transport.APIAttemptCapture)

// Stream performs a streaming exchange: a 2xx response is handed to decode
// after STREAM_START is published; a non-2xx response is classified and
// returned and never reaches decode.
func Stream(parentCtx context.Context, c *Call, decode StreamDecoder) (llm.Stream, error) {
	sctx, cancel := context.WithCancel(parentCtx)
	sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, c.Req.AdapterTimeout, true)
	cancelAll := func() {
		cancel()
		timeoutCancel()
	}
	p, err := Prepare(sctx, c)
	if err != nil {
		cancelAll()
		return nil, err
	}
	client := llm.ClientWithAdapterTimeout(c.httpClient(), c.Req.AdapterTimeout)
	resp, attempt, err := transport.DoWithAPIAttempts(parentCtx, client, p.Request, c.metaBuilder(p))
	if err != nil {
		returned := llm.WrapContextError(c.Res.Instance, err)
		attempt.Complete(llm.APIAttemptResult{Err: returned}, llm.APITimeoutSourceForTransport(parentCtx, sctx, err), nil, err)
		cancelAll()
		return nil, returned
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, readErr := io.ReadAll(resp.Body)
		returned := c.classify(resp.StatusCode, resp.Header, rawBytes)
		var raw map[string]any
		decodeErr := json.Unmarshal(rawBytes, &raw)
		if readErr != nil {
			decodeErr = readErr
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, ResponseBody: rawBytes, Err: returned}, llm.APITimeoutNone, decodeErr, nil)
		cancelAll()
		return nil, returned
	}
	s := llm.NewChanStream(cancelAll)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
	r := &Result{StatusCode: resp.StatusCode, Header: resp.Header, EndpointURL: llm.FinalResponseEndpointURL(resp, c.URL), Material: p.material, PrunedFields: p.PrunedFields}
	go decode(sctx, cancelAll, resp, s, r, attempt)
	return s, nil
}
