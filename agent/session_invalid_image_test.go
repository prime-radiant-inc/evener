package agent

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

func TestInvalidRasterToolResultRemainsRecoverable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.png")
	if err := os.WriteFile(path, pngWithExtraScanline(t), 0o600); err != nil {
		t.Fatalf("write malformed PNG: %v", err)
	}

	var requestErr error
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "read_invalid_image",
					Name:      "read_file",
					Type:      "function",
					Arguments: json.RawMessage(`{"file_path":"frame.png","purpose":"inspect it"}`),
				})
			},
			func(req llm.Request) llm.Response {
				result := findRequestToolResult(req.Messages, "read_invalid_image")
				switch {
				case result == nil:
					requestErr = fmt.Errorf("next request has no tool result: %+v", req.Messages)
				case !result.IsError:
					requestErr = fmt.Errorf("invalid image tool result is not an error: %+v", result)
				case len(result.ImageData) != 0 || result.ImageMediaType != "":
					requestErr = fmt.Errorf("invalid image bytes reached the next request: %+v", result)
				case !strings.Contains(fmt.Sprint(result.Content), "invalid image data"):
					requestErr = fmt.Errorf("tool error does not explain the invalid image: %+v", result)
				}
				return finalResponse("recovered")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{AgentName: "explorer"},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	drainSessionEvents(sess)

	out, err := sess.ProcessInput(context.Background(), "inspect frame.png", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if strings.TrimSpace(out) != "recovered" {
		t.Fatalf("output = %q, want recovered", out)
	}
}

func findRequestToolResult(messages []llm.Message, callID string) *llm.ToolResultData {
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return part.ToolResult
			}
		}
	}
	return nil
}

func pngWithExtraScanline(t *testing.T) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte{
		0, 0, 0, 0,
		0, 0, 0, 0,
	}); err != nil {
		t.Fatalf("compress malformed PNG pixels: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close malformed PNG compressor: %v", err)
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 1)
	binary.BigEndian.PutUint32(ihdr[4:8], 1)
	ihdr[8] = 8
	ihdr[9] = 2

	data := append([]byte{}, []byte("\x89PNG\r\n\x1a\n")...)
	data = appendPNGChunk(data, "IHDR", ihdr)
	data = appendPNGChunk(data, "IDAT", compressed.Bytes())
	return appendPNGChunk(data, "IEND", nil)
}

func appendPNGChunk(dst []byte, kind string, data []byte) []byte {
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(data)))
	dst = append(dst, length...)
	dst = append(dst, kind...)
	dst = append(dst, data...)
	checksum := crc32.ChecksumIEEE(append([]byte(kind), data...))
	binary.BigEndian.PutUint32(length, checksum)
	return append(dst, length...)
}
