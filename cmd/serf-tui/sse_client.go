package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const maxSSELineBytes = 2 * 1024 * 1024

func newSSEScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxSSELineBytes)
	return scanner
}

// SSEEvent represents a parsed Server-Sent Event.
type SSEEvent struct {
	ID    string
	Event string
	Data  string
}

// parseLine applies a single SSE line to the current event being built.
func (e *SSEEvent) parseLine(line string) {
	if line == "" || strings.HasPrefix(line, ":") {
		return
	}
	field, value, ok := strings.Cut(line, ":")
	if !ok {
		field = line
		value = ""
	}
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	switch field {
	case "id":
		e.ID = value
	case "event":
		e.Event = value
	case "data":
		if e.Data != "" {
			e.Data += "\n"
		}
		e.Data += value
	}
}

// hasContent reports whether the event has any parsed fields.
func (e *SSEEvent) hasContent() bool {
	return e.Event != "" || e.Data != ""
}

// parseSSEStream reads all SSE events from a reader.
func parseSSEStream(r io.Reader) ([]SSEEvent, error) {
	var events []SSEEvent
	scanner := newSSEScanner(r)
	var current SSEEvent
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if current.hasContent() {
				events = append(events, current)
				current = SSEEvent{}
			}
			continue
		}
		current.parseLine(line)
	}
	if current.hasContent() {
		events = append(events, current)
	}
	return events, scanner.Err()
}

// sseEventMsg is a Bubble Tea message wrapping a parsed SSE event.
type sseEventMsg SSEEvent

// sseConnectedMsg signals successful SSE connection.
type sseConnectedMsg struct{}

// sseErrorMsg signals an SSE connection error.
type sseErrorMsg struct{ err error }

// streamSSE connects to the SSE endpoint and sends events as Bubble Tea
// messages. It blocks until the stream ends or context is cancelled.
func streamSSE(ctx context.Context, addr string, send func(tea.Msg)) {
	url := fmt.Sprintf("http://%s/events", addr)
	streamSSEURL(ctx, url, "", send)
}

func streamSSEURL(ctx context.Context, url, lastEventID string, send func(tea.Msg)) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		send(sseErrorMsg{err})
		return
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		send(sseErrorMsg{err})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		send(sseErrorMsg{fmt.Errorf("SSE stream returned %d", resp.StatusCode)})
		return
	}

	send(sseConnectedMsg{})

	scanner := newSSEScanner(resp.Body)
	var current SSEEvent
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if current.hasContent() {
				send(sseEventMsg(current))
				current = SSEEvent{}
			}
			continue
		}
		current.parseLine(line)
	}
	if current.hasContent() {
		send(sseEventMsg(current))
	}
	if err := scanner.Err(); err != nil {
		send(sseErrorMsg{err})
		return
	}
	send(sseErrorMsg{fmt.Errorf("SSE stream closed")})
}
