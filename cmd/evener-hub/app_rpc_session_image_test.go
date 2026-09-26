package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// sessionImageTestSession is a real session identifier: the file-backed branch
// resolves its working directory through the same local-route validation the
// HTTP handlers use, so a hand-written placeholder would not exercise it.
var sessionImageTestSession = identifier.MustNewSessionID()

// sessionImageTestPNG is a minimal byte string whose declared content type is
// image/png. The sha-addressed fixture stores a deliberately WRONG media type
// alongside it so a response that trusted the transcript would disagree with
// one re-derived from the bytes.
var sessionImageTestPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 's', 'h', 'o', 't'}

// sessionImageTurnFixture is one tool-result image turn the transcript fixture
// appends: the bytes and the media type stored alongside them.
type sessionImageTurnFixture struct {
	image           []byte
	storedMediaType string
}

// seedSessionImageSession writes one session whose transcript holds a tool-result
// image (plus any extra turns), and indexes it into a fresh past index. cwd is
// the working directory the session meta records (the containment root of the
// file-backed branch).
func seedSessionImageSession(t *testing.T, cwd string, image []byte, storedMediaType string, extra ...sessionImageTurnFixture) *hubcore.PastIndex {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "projects", "session-image-0123456789")
	if err := os.MkdirAll(filepath.Join(project, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID:        sessionImageTestSession,
		UpdatedAt: time.Now(),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(
		filepath.Join(project, "sessions", sessionImageTestSession+".transcript.jsonl"),
		transcript.Header{SessionID: sessionImageTestSession, ProfileID: "openai", Model: "gpt-5"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, turn := range append([]sessionImageTurnFixture{{image: image, storedMediaType: storedMediaType}}, extra...) {
		if err := w.Append(schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, ToolCallID: fmt.Sprintf("call_shot_%d", index), Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: fmt.Sprintf("call_shot_%d", index), Name: "screenshot", Content: "captured",
					ImageData: turn.image, ImageMediaType: turn.storedMediaType,
				},
			}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return past
}

func requestSessionImage(t *testing.T, srv *httptest.Server, params appwire.SessionImageParams) (appwire.SessionImageResponse, error) {
	t.Helper()
	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.SessionImageResponse
	err := rpc.Request(context.Background(), appwire.MethodEvenerSessionImage, params, &resp)
	return resp, err
}

// sessionImageErrorInfo names the typed ErrorInfo a refusal carries, the same
// spelling the controller's route maps to an HTTP status.
func sessionImageErrorInfo(t *testing.T, err error) string {
	t.Helper()
	wire, ok := wireErrorFromError(err)
	if !ok {
		t.Fatalf("error %T = %v, want appwire.WireError", err, err)
	}
	return evenerErrorInfoFromData(wire.Data)
}

// The host-side evener/session/image method is the AppWire counterpart of the
// local /s/<id>/images/<sha> route: it re-scans the session transcript and
// answers the matched bytes, their re-derived media type, the byte length, and
// the lowercase hex sha256. The stored transcript media type is never trusted.
func TestHubSessionImageServesShaAddressedBytes(t *testing.T) {
	past := seedSessionImageSession(t, "", sessionImageTestPNG, "text/plain")
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
	defer srv.Close()

	resp, err := requestSessionImage(t, srv, appwire.SessionImageParams{
		SessionID: sessionImageTestSession,
		SHA:       imageSha(sessionImageTestPNG),
	})
	if err != nil {
		t.Fatalf("evener/session/image: %v", err)
	}
	if !bytes.Equal(resp.Data, sessionImageTestPNG) {
		t.Fatalf("Data = %q, want the transcript image bytes", resp.Data)
	}
	if resp.Size != int64(len(sessionImageTestPNG)) {
		t.Fatalf("Size = %d, want %d", resp.Size, len(sessionImageTestPNG))
	}
	if resp.SHA != imageSha(sessionImageTestPNG) {
		t.Fatalf("SHA = %q, want %q", resp.SHA, imageSha(sessionImageTestPNG))
	}
	if resp.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want the bytes' own image/png, not the transcript's stored text/plain", resp.MediaType)
	}
}

// The file-backed branch resolves the session's working directory from the
// host's own past index and reads a session-relative path inside it, with the
// same containment and 8 MiB bound the local /doc/image route uses.
func TestHubSessionImageServesFileBackedBytes(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), sessionImageTestPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
	defer srv.Close()

	resp, err := requestSessionImage(t, srv, appwire.SessionImageParams{
		SessionID: sessionImageTestSession,
		Path:      "shot.png",
	})
	if err != nil {
		t.Fatalf("evener/session/image: %v", err)
	}
	if !bytes.Equal(resp.Data, sessionImageTestPNG) {
		t.Fatalf("Data = %q, want the file's bytes", resp.Data)
	}
	if resp.Size != int64(len(sessionImageTestPNG)) || resp.SHA != imageSha(sessionImageTestPNG) {
		t.Fatalf("Size/SHA = %d/%q, want %d/%q", resp.Size, resp.SHA, len(sessionImageTestPNG), imageSha(sessionImageTestPNG))
	}
	if resp.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", resp.MediaType)
	}
}

// Every refusal the contract names is typed: malformed or ambiguous request
// fields are InvalidParams, and anything the host cannot resolve — unknown
// session, missing transcript, unresolvable path or sha, unsupported media,
// an over-bound image or record — is ResourceNotFound. Nothing is guessed and
// nothing is served.
func TestHubSessionImageRefusals(t *testing.T) {
	oversize := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, outputImageMaxBytes/4+1)
	// Large enough that the record's own base64 form exceeds the record bound, so
	// the read refuses it before DecodeEntry rather than at the image bound.
	overRecord := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 12*1024*1024/4)
	assertOverRecordBound(t, overRecord)

	t.Run("sha and path are mutually exclusive", func(t *testing.T) {
		past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			SHA:       imageSha(sessionImageTestPNG),
			Path:      "shot.png",
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorInvalidParams) {
			t.Fatalf("both selectors = %q, want invalidParams", got)
		}
	})

	t.Run("one selector is required", func(t *testing.T) {
		past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{SessionID: sessionImageTestSession})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorInvalidParams) {
			t.Fatalf("neither selector = %q, want invalidParams", got)
		}
	})

	t.Run("sha must be lowercase hex", func(t *testing.T) {
		past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			SHA:       strings.ToUpper(imageSha(sessionImageTestPNG)),
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorInvalidParams) {
			t.Fatalf("uppercase sha = %q, want invalidParams", got)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: "02wMissingSession0000000000",
			SHA:       imageSha(sessionImageTestPNG),
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("unknown session = %q, want resourceNotFound", got)
		}
	})

	t.Run("sha resolves to nothing", func(t *testing.T) {
		past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			SHA:       strings.Repeat("b", 64),
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("unknown sha = %q, want resourceNotFound", got)
		}
	})

	t.Run("escaping path is refused, not served", func(t *testing.T) {
		cwd := t.TempDir()
		outside := filepath.Join(filepath.Dir(cwd), "outside.png")
		if err := os.WriteFile(outside, sessionImageTestPNG, 0o644); err != nil {
			t.Fatal(err)
		}
		inside := filepath.Join(cwd, "shot.png")
		if err := os.WriteFile(inside, sessionImageTestPNG, 0o644); err != nil {
			t.Fatal(err)
		}
		past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		// An absolute path is refused even when it happens to sit inside the
		// session root: Path is a session-relative path by contract.
		for _, rel := range []string{"../outside.png", outside, "a/../../outside.png", inside} {
			_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
				SessionID: sessionImageTestSession,
				Path:      rel,
			})
			if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorInvalidParams) {
				t.Fatalf("path %q = %q, want invalidParams", rel, got)
			}
		}
	})

	t.Run("missing file", func(t *testing.T) {
		past := seedSessionImageSession(t, t.TempDir(), sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			Path:      "missing.png",
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("missing file = %q, want resourceNotFound", got)
		}
	})

	t.Run("non-regular file", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.Mkdir(filepath.Join(cwd, "dir.png"), 0o755); err != nil {
			t.Fatal(err)
		}
		past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			Path:      "dir.png",
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("directory = %q, want resourceNotFound", got)
		}
	})

	t.Run("unsupported media", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "notes.png"), []byte("not an image"), 0o644); err != nil {
			t.Fatal(err)
		}
		past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			Path:      "notes.png",
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("unsupported media = %q, want resourceNotFound", got)
		}
	})

	t.Run("over-bound file is refused at stat time", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "big.png"), oversize, 0o644); err != nil {
			t.Fatal(err)
		}
		past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			Path:      "big.png",
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("over-bound file = %q, want resourceNotFound", got)
		}
	})

	t.Run("over-bound transcript image is refused", func(t *testing.T) {
		past := seedSessionImageSession(t, "", oversize, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			SHA:       imageSha(oversize),
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("over-bound transcript image = %q, want resourceNotFound", got)
		}
	})

	t.Run("over-bound transcript record is refused while scanning", func(t *testing.T) {
		past := seedSessionImageSession(t, "", overRecord, "image/png")
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
		defer srv.Close()
		_, err := requestSessionImage(t, srv, appwire.SessionImageParams{
			SessionID: sessionImageTestSession,
			SHA:       imageSha(overRecord),
		})
		if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
			t.Fatalf("over-bound transcript record = %q, want resourceNotFound", got)
		}
	})
}

// A transcript can hold an over-bound record after the one that carries the
// requested image — a huge later tool result, for example. The bounded read
// refuses a record it cannot decode, but that refusal is about what it would
// have to read next, not about an answer it already found: the image is served.
func TestHubSessionImageServesAMatchBeforeAnOverBoundTail(t *testing.T) {
	tail := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 12*1024*1024/4)
	assertOverRecordBound(t, tail)
	past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png",
		sessionImageTurnFixture{image: tail, storedMediaType: "image/png"})
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
	defer srv.Close()

	resp, err := requestSessionImage(t, srv, appwire.SessionImageParams{
		SessionID: sessionImageTestSession,
		SHA:       imageSha(sessionImageTestPNG),
	})
	if err != nil {
		t.Fatalf("evener/session/image: %v", err)
	}
	if !bytes.Equal(resp.Data, sessionImageTestPNG) {
		t.Fatalf("Data = %q, want the matched image's bytes", resp.Data)
	}
	if resp.SHA != imageSha(sessionImageTestPNG) || resp.MediaType != "image/png" {
		t.Fatalf("SHA/MediaType = %q/%q, want the matched image's own", resp.SHA, resp.MediaType)
	}
}

// assertOverRecordBound fails unless an image of this size encodes past the
// record bound the bounded scan reads under: an under-bound fixture would reach
// DecodeEntry and exercise the image bound instead, leaving the record-level
// refusal untested.
func assertOverRecordBound(t *testing.T, image []byte) {
	t.Helper()
	maxRecordBytes, _ := sessionImageBounds()
	if encoded := base64.StdEncoding.EncodedLen(len(image)); encoded <= maxRecordBytes {
		t.Fatalf("fixture image of %d bytes encodes to %d, at or under the record bound %d: it would not exercise the record-level refusal", len(image), encoded, maxRecordBytes)
	}
}
