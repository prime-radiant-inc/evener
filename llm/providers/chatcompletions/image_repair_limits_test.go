package chatcompletions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/internal/imagerepair"
	"primeradiant.com/evener/llm/registry"
)

// noHTTPServer fails the test the moment any request reaches it, so a test
// proving "the error is returned before the wire" cannot silently start doing
// network I/O.
func noHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no HTTP exchange was expected: the error must short-circuit the call")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCompleteRejectsUnshapeableToolChoice pins the build error surfacing from
// the recovery loop's own body build: a named tool choice without a name never
// reaches the wire.
func TestCompleteRejectsUnshapeableToolChoice(t *testing.T) {
	srv := noHTTPServer(t)
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, liveRes(srv, nil))
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("err = %v, want the structured configuration error", err)
	}
}

// TestImageRecoveryUnserializableBodyFailsBeforeTheWire pins the prepare
// marshal guard: a provider option the body cannot serialize fails the call
// before any request is sent — no retry, no partial body.
func TestImageRecoveryUnserializableBodyFailsBeforeTheWire(t *testing.T) {
	srv := noHTTPServer(t)
	req := userReq("hi")
	req.ProviderOptions = map[string]any{registry.ProtocolOpenAIChat: map[string]any{"fuzz_opt": make(chan int)}}
	if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, liveRes(srv, nil)); err == nil {
		t.Fatal("an unserializable provider option must fail the call")
	}
}

// TestImageRecoveryUndecodableMessagesFallsBackToOriginal pins the repair
// decode guard: when the final wire's messages cannot even be re-decoded, the
// repair disables itself and the caller gets the provider's original error
// back — one request, no resends.
func TestImageRecoveryUndecodableMessagesFallsBackToOriginal(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, imageRejection)
	}))
	t.Cleanup(srv.Close)
	req := recoveryRequest(recoveryJPEG(t, nil))
	req.ProviderOptions = map[string]any{registry.ProtocolOpenAIChat: map[string]any{"messages": "not-an-array"}}
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, liveRes(srv, nil))
	if calls != 1 {
		t.Fatalf("requests = %d, want the single original attempt", calls)
	}
	var providerErr llm.Error
	if !errors.As(err, &providerErr) || providerErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("err = %v, want the provider's original 400 back", err)
	}
}

// TestImageRecoveryCorruptJPEGSurfacesRepairError pins the repair error
// propagation: a claimed-JPEG image whose bytes the repair cannot decode
// surfaces the repair failure instead of resending forever.
func TestImageRecoveryCorruptJPEGSurfacesRepairError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, imageRejection)
	}))
	t.Cleanup(srv.Close)
	req := recoveryRequest([]byte("not a jpeg at all"))
	if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, liveRes(srv, nil)); err == nil {
		t.Fatal("an undecodable image must surface the repair failure")
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want the repair to fail without resending", calls)
	}
}

// TestImageRecoveryProfileFreeKeepsTrailingPart pins the caveat insertion
// walk: when the profile-free stage removes a color profile, the caveat text
// part is inserted before the repaired image and every later part of the same
// message still rides along after it.
func TestImageRecoveryProfileFreeKeepsTrailingPart(t *testing.T) {
	profile, err := os.ReadFile("../../internal/imagerepair/testdata/p3.icc")
	if err != nil {
		t.Fatal(err)
	}
	original := recoveryWithProfile(recoveryNoisyJPEG(t), profile)
	req := userReq("sentinel-input")
	req.Messages[0].Content = []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: original, MediaType: "image/jpeg"}},
		{Kind: llm.ContentText, Text: "tail-note"},
	}
	calls := 0
	var lastContent []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		if err := dec.Decode(&body); err != nil {
			t.Error(err)
		}
		if messages, _ := body["messages"].([]any); len(messages) > 0 {
			lastContent, _ = messages[0].(map[string]any)["content"].([]any)
		}
		switch calls {
		case 1, 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, imageRejection)
		default:
			recoveryReply(w, false)
		}
	}))
	t.Cleanup(srv.Close)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("requests = %d, want the three recovery attempts", calls)
	}
	if len(lastContent) != 3 {
		t.Fatalf("repaired content = %v, want caveat, image, and the trailing text part", lastContent)
	}
	first, _ := lastContent[0].(map[string]any)
	second, _ := lastContent[1].(map[string]any)
	third, _ := lastContent[2].(map[string]any)
	if first["type"] != "text" || first["text"] != unmanagedImageCaveat {
		t.Fatalf("first part = %v, want the color caveat text", first)
	}
	if second["type"] != "image_url" {
		t.Fatalf("second part = %v, want the repaired image", second)
	}
	if third["type"] != "text" || third["text"] != "tail-note" {
		t.Fatalf("third part = %v, want the trailing text part kept after the repair", third)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Code != "image_color_unmanaged" {
		t.Fatalf("warnings = %+v", resp.Warnings)
	}
}

// TestProviderStatusExtractsTypedStatus pins the helper the expansion check
// reads: a typed provider error yields its status, anything else yields none.
func TestProviderStatusExtractsTypedStatus(t *testing.T) {
	typed := llm.ErrorFromHTTPStatus("work", http.StatusRequestEntityTooLarge, "too big", nil, nil)
	if got := providerStatus(typed); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("providerStatus(typed) = %d", got)
	}
	if got := providerStatus(errors.New("plain")); got != 0 {
		t.Fatalf("providerStatus(plain) = %d, want 0", got)
	}
}

// TestRepairMessagesMarshalFailureDisablesRepair pins the repair's own guard:
// a messages array that cannot be serialized reports "repair produced no
// distinct usable request" instead of guessing.
func TestRepairMessagesMarshalFailureDisablesRepair(t *testing.T) {
	changed, unmanaged, managed, err := repairMessages(t.Context(), map[string]any{
		"messages": []any{make(chan int)},
	}, imagerepair.PreferColorManaged)
	if changed || unmanaged || managed {
		t.Fatalf("changed=%v unmanaged=%v managed=%v, want no repair", changed, unmanaged, managed)
	}
	if !errors.Is(err, errImageRepairUnchanged) {
		t.Fatalf("err = %v, want the repair-unchanged sentinel", err)
	}
}

// TestRepairImageURISizeCap pins the repair's allocation bound: a jpeg data
// URI whose base64 exceeds the 32MiB repair limit is left alone — the repair
// declines rather than decoding it.
func TestRepairImageURISizeCap(t *testing.T) {
	uri := "data:image/jpeg;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(32<<20)+1)
	result, err := repairImageURI(t.Context(), uri, imagerepair.PreferColorManaged, new(int64(32_000_000)))
	if err != nil {
		t.Fatalf("an oversized URI must be declined, not failed: %v", err)
	}
	if result.Method != imagerepair.Unchanged {
		t.Fatalf("result = %+v, want Unchanged", result)
	}
}
