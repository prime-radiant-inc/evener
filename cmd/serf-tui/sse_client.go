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

// SSEEvent represents a parsed Server-Sent Event.
type SSEEvent struct {
	ID    string
	Event string
	Data  string
}

// parseLine applies a single SSE line to the current event being built.
func (e *SSEEvent) parseLine(line string) {
	if strings.HasPrefix(line, "id: ") {
		e.ID = strings.TrimPrefix(line, "id: ")
	} else if strings.HasPrefix(line, "event: ") {
		e.Event = strings.TrimPrefix(line, "event: ")
	} else if strings.HasPrefix(line, "data: ") {
		e.Data = strings.TrimPrefix(line, "data: ")
	}
}

// hasContent reports whether the event has any parsed fields.
func (e *SSEEvent) hasContent() bool {
	return e.Event != "" || e.Data != ""
}

// parseSSEStream reads all SSE events from a reader.
func parseSSEStream(r io.Reader) ([]SSEEvent, error) {
	var events []SSEEvent
	scanner := bufio.NewScanner(r)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		send(sseErrorMsg{err})
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		send(sseErrorMsg{err})
		return
	}
	defer resp.Body.Close()

	send(sseConnectedMsg{})

	scanner := bufio.NewScanner(resp.Body)
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
	send(sseErrorMsg{fmt.Errorf("SSE stream closed")})
}
