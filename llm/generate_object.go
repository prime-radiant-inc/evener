package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

type GenerateObjectOptions struct {
	GenerateOptions
	Schema map[string]any
	Strict bool
}

func GenerateObject(ctx context.Context, opts GenerateObjectOptions) (*GenerateResult, error) {
	if opts.Schema == nil {
		return nil, &ConfigurationError{Message: "schema is required"}
	}
	strict := opts.Strict
	if !opts.Strict {
		// default
		strict = true
	}

	// Provider-specific structured output configuration is handled at the adapter layer.
	ro := opts.GenerateOptions
	ro.ResponseFormat = &ResponseFormat{
		Type:       "json_schema",
		JSONSchema: opts.Schema,
		Strict:     strict,
	}
	res, err := Generate(ctx, ro)
	if err != nil {
		return nil, err
	}

	var out any
	dec := json.NewDecoder(bytes.NewReader([]byte(res.Text)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, NewNoObjectGeneratedError(fmt.Sprintf("failed to parse JSON output: %v", err), res.Text)
	}

	schema, err := compileJSONSchema(opts.Schema)
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(out); err != nil {
		return nil, NewNoObjectGeneratedError(fmt.Sprintf("JSON output failed schema validation: %v", err), res.Text)
	}
	res.Output = out
	return res, nil
}

func compileJSONSchema(schema map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	if err := c.AddResource("schema.json", bytes.NewReader(b)); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

// StreamObjectResult wraps a StreamResult and adds schema-validated output.
type StreamObjectResult struct {
	inner     *StreamResult
	outStream *ChanStream

	mu     sync.Mutex
	output any
	objErr error
	done   chan struct{}
}

// Output returns the parsed and validated object after the stream completes.
// Must be called after draining Events().
func (r *StreamObjectResult) Output() any {
	if r == nil {
		return nil
	}
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.output
}

// Events returns the event channel from the wrapping stream, which includes OBJECT_DELTA events.
func (r *StreamObjectResult) Events() <-chan StreamEvent { return r.outStream.Events() }

// Close closes the wrapping stream.
func (r *StreamObjectResult) Close() error { return r.outStream.Close() }

// Response returns the inner stream response, or any object parse/validation error.
func (r *StreamObjectResult) Response() (*Response, error) {
	if r == nil {
		return nil, fmt.Errorf("stream object result is nil")
	}
	<-r.done
	resp, err := r.inner.Response()
	r.mu.Lock()
	objErr := r.objErr
	r.mu.Unlock()
	if objErr != nil {
		return resp, objErr
	}
	return resp, err
}

// StreamGenerateObject is the streaming equivalent of GenerateObject. It wraps
// StreamGenerate and adds incremental JSON parsing, emitting OBJECT_DELTA events
// as the streamed text becomes parseable. The final output is validated against
// the schema.
func StreamGenerateObject(ctx context.Context, opts GenerateObjectOptions) (*StreamObjectResult, error) {
	if opts.Schema == nil {
		return nil, &ConfigurationError{Message: "schema is required"}
	}
	strict := opts.Strict
	if !opts.Strict {
		strict = true
	}

	compiledSchema, err := compileJSONSchema(opts.Schema)
	if err != nil {
		return nil, err
	}

	ro := opts.GenerateOptions
	ro.ResponseFormat = &ResponseFormat{
		Type:       "json_schema",
		JSONSchema: opts.Schema,
		Strict:     strict,
	}

	inner, err := StreamGenerate(ctx, ro)
	if err != nil {
		return nil, err
	}

	_, cancelOut := context.WithCancel(ctx)
	outStream := NewChanStream(cancelOut)
	res := &StreamObjectResult{
		inner:     inner,
		outStream: outStream,
		done:      make(chan struct{}),
	}

	go func() {
		defer close(res.done)
		defer outStream.CloseSend()
		var buf strings.Builder

		for ev := range inner.Events() {
			if ev.Type == StreamEventTextDelta {
				buf.WriteString(ev.Delta)
				if obj := tryParsePartialJSON(buf.String()); obj != nil {
					outStream.Send(StreamEvent{
						Type:        StreamEventObjectDelta,
						ObjectDelta: obj,
					})
				}
			}
			outStream.Send(ev)
		}

		// After the inner stream finishes, validate the final text.
		resp, respErr := inner.Response()
		if respErr != nil {
			return
		}

		text := buf.String()
		if text == "" && resp != nil {
			text = resp.Text()
		}

		var out any
		dec := json.NewDecoder(bytes.NewReader([]byte(text)))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			res.mu.Lock()
			res.objErr = NewNoObjectGeneratedError(fmt.Sprintf("failed to parse JSON output: %v", err), text)
			res.mu.Unlock()
			return
		}

		if err := compiledSchema.Validate(out); err != nil {
			res.mu.Lock()
			res.objErr = NewNoObjectGeneratedError(fmt.Sprintf("JSON output failed schema validation: %v", err), text)
			res.mu.Unlock()
			return
		}

		res.mu.Lock()
		res.output = out
		res.mu.Unlock()
	}()

	return res, nil
}

// tryParsePartialJSON attempts to parse a potentially incomplete JSON string.
// It tries the string as-is first, then tries appending closing braces/brackets.
func tryParsePartialJSON(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	// Try as-is first.
	if obj := tryUnmarshal(s); obj != nil {
		return obj
	}

	// Count unbalanced braces/brackets and try closing them.
	var closers []byte
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			closers = append(closers, '}')
		case '[':
			closers = append(closers, ']')
		case '}', ']':
			if len(closers) > 0 {
				closers = closers[:len(closers)-1]
			}
		}
	}

	if len(closers) == 0 {
		return nil
	}

	// If we're in a string, close it first.
	attempt := s
	if inString {
		attempt += `"`
	}

	// Strip trailing comma before closing.
	attempt = strings.TrimRight(attempt, " \t\n\r,")

	// Append closers in reverse order.
	for i := len(closers) - 1; i >= 0; i-- {
		attempt += string(closers[i])
	}

	return tryUnmarshal(attempt)
}

func tryUnmarshal(s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}
