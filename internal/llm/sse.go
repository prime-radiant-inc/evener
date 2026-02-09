package llm

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
)

type SSEEvent struct {
	Event string
	Data  []byte
}

// ParseSSE parses Server-Sent Events from r and invokes fn for each complete event.
// It handles "event:" and "data:" lines and emits an event on blank-line boundaries.
func ParseSSE(ctx context.Context, r io.Reader, fn func(ev SSEEvent) error) error {
	br := bufio.NewReader(r)

	var curEvent string
	var dataBuf bytes.Buffer
	flush := func() error {
		if curEvent == "" && dataBuf.Len() == 0 {
			return nil
		}
		b := bytes.TrimSuffix(dataBuf.Bytes(), []byte("\n"))
		ev := SSEEvent{Event: curEvent, Data: append([]byte{}, b...)}
		curEvent = ""
		dataBuf.Reset()
		return fn(ev)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			if err := flush(); err != nil {
				return err
			}
		} else if strings.HasPrefix(line, ":") {
			// Comment; ignore.
		} else if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimLeft(data, " ")
			dataBuf.WriteString(data)
			dataBuf.WriteString("\n")
		} else if strings.HasPrefix(line, "retry:") {
			// Ignore reconnection hint.
		} else {
			// Unknown field; ignore.
		}

		if err == io.EOF {
			// Flush final partial event.
			return flush()
		}
	}
}
