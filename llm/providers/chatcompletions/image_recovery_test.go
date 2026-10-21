package chatcompletions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

const imageRejection = `{"error":{"message":"Image normalization failed","type":"invalid_request_error"}}`

func recoveryJPEG(t *testing.T, profile []byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for y := range 2 {
		for x := range 3 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(30 + x*70), G: uint8(40 + y*120), B: 60, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return recoveryWithProfile(buf.Bytes(), profile)
}

func recoveryWithProfile(data, profile []byte) []byte {
	if profile != nil {
		payload := append([]byte("ICC_PROFILE\x00\x01\x01"), profile...)
		segment := []byte{0xff, 0xe2, 0, 0}
		binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
		data = append(append(append([]byte{}, data[:2]...), append(segment, payload...)...), data[2:]...)
	}
	return data
}

func recoveryURI(data []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
}

func recoveryNoisyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var seed uint32 = 19
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = byte(seed >> 24)
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func recoveryRequest(data []byte) llm.Request {
	req := userReq("sentinel-input")
	req.Messages[0].Content = append(req.Messages[0].Content, llm.ContentPart{
		Kind: llm.ContentImage, Image: &llm.ImageData{Data: data, MediaType: "image/jpeg"},
	})
	return req
}

func recoveryCall(ctx context.Context, p *Protocol, req llm.Request, res registry.Resolved, stream bool) (llm.Response, error) {
	if !stream {
		return p.Complete(ctx, req, res)
	}
	s, err := p.Stream(ctx, req, res)
	if err != nil {
		return llm.Response{}, err
	}
	var resp llm.Response
	for event := range s.Events() {
		if event.Type == llm.StreamEventError {
			return resp, event.Err
		}
		if event.Response != nil {
			resp = *event.Response
		}
	}
	return resp, nil
}

func recoveryReply(w http.ResponseWriter, stream bool) {
	body := chatJSON
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		body = chatSSE
	}
	_, _ = io.WriteString(w, body)
}

func recoveryImages(t *testing.T, body map[string]any) []string {
	t.Helper()
	var urls []string
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].([]any)
		for _, partRaw := range content {
			part, _ := partRaw.(map[string]any)
			if part["type"] != "image_url" {
				continue
			}
			imageURL, _ := part["image_url"].(map[string]any)
			url, _ := imageURL["url"].(string)
			urls = append(urls, url)
		}
	}
	return urls
}

func TestImageRecoveryManaged(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			data := recoveryJPEG(t, nil)
			req := recoveryRequest(data)
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var requests []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				requests = append(requests, body)
				if len(requests) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, imageRejection)
					return
				}
				recoveryReply(w, stream)
			}))
			defer srv.Close()
			sink := &captureSink{}
			ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_image_recovery")), sink)
			resp, err := recoveryCall(ctx, &Protocol{Client: srv.Client()}, req, liveRes(srv, nil), stream)
			if err != nil {
				t.Fatal(err)
			}
			records := sink.records()
			if len(records) != 2 || records[0].Response == nil || records[0].Response.StatusCode == nil || *records[0].Response.StatusCode != 400 || records[1].Outcome != apilog.AttemptSuccess {
				t.Fatalf("attempt records=%+v", records)
			}
			if len(requests) != 2 || len(resp.ToolCalls()) != 1 || len(resp.Warnings) != 0 {
				t.Fatalf("requests=%d response=%+v", len(requests), resp)
			}
			if got := recoveryImages(t, requests[0]); !reflect.DeepEqual(got, []string{recoveryURI(data)}) {
				t.Fatal("initial image changed")
			}
			urls := recoveryImages(t, requests[1])
			if len(urls) != 1 || !strings.HasPrefix(urls[0], "data:image/png;base64,") {
				t.Fatalf("repaired image types/count: %v", urls)
			}
			pngBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(urls[0], "data:image/png;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(pngBytes))
			if err != nil || img.Bounds() != image.Rect(0, 0, 3, 2) {
				t.Fatalf("repaired geometry: image=%v error=%v", img, err)
			}
			for _, body := range requests {
				if body["model"] != "m-wire" {
					t.Fatal("model changed")
				}
			}
			after, _ := json.Marshal(req)
			if !bytes.Equal(before, after) {
				t.Fatal("caller request changed")
			}
		})
	}
}

func TestImageRecoveryNoUnchangedResend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		uri  string
	}{
		{"remote", "https://example.invalid/photo.jpg"},
		{"unsupported", "data:image/png;base64,iVBORw0KGgo="},
		{"malformed", "data:image/jpeg;base64,bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, imageRejection)
			}))
			defer srv.Close()
			req := recoveryRequest(nil)
			req.Messages[0].Content[1].Image.URL = tc.uri
			_, err := recoveryCall(t.Context(), &Protocol{Client: srv.Client()}, req, liveRes(srv, nil), false)
			var providerErr llm.Error
			if calls != 1 || !errors.As(err, &providerErr) || providerErr.StatusCode() != 400 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestImageRecoveryFallbackAndExhaustion(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			profile    []byte
			statuses   []int
			wantStatus int
			wantWarn   bool
		}{
			{"unsupported profile", []byte("unsupported-icc"), []int{400, 200}, 0, true},
			{"no duplicate fallback", []byte("unsupported-icc"), []int{400, 422}, 422, false},
			{"repair expansion", nil, []int{400, 413}, 400, false},
			{"unrelated failure", nil, []int{401}, 401, false},
		} {
			t.Run(tc.name+map[bool]string{false: "/complete", true: "/stream"}[stream], func(t *testing.T) {
				data := recoveryJPEG(t, tc.profile)
				if tc.profile == nil {
					data = recoveryNoisyJPEG(t)
				}
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					if calls > len(tc.statuses) {
						t.Error("repair exceeded expected request budget")
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					status := tc.statuses[calls-1]
					if status == 200 {
						recoveryReply(w, stream)
						return
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, imageRejection)
				}))
				defer srv.Close()
				resp, err := recoveryCall(t.Context(), &Protocol{Client: srv.Client()}, recoveryRequest(data), liveRes(srv, nil), stream)
				if calls != len(tc.statuses) {
					t.Fatalf("requests=%d, want %d, error=%v", calls, len(tc.statuses), err)
				}
				if tc.wantStatus != 0 {
					var providerErr llm.Error
					if !errors.As(err, &providerErr) || providerErr.StatusCode() != tc.wantStatus {
						t.Fatalf("terminal error=%v, want status=%d", err, tc.wantStatus)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if tc.wantWarn && (len(resp.Warnings) != 1 || resp.Warnings[0].Code != "image_color_unmanaged") {
					t.Fatalf("warnings=%+v", resp.Warnings)
				}
			})
		}
	}
}

type imageRecoveryPreparer struct{ messages any }

func (imageRecoveryPreparer) Apply(context.Context, *http.Request, registry.Resolved) error {
	return nil
}

func (p imageRecoveryPreparer) PrepareRequest(_ context.Context, _ *http.Request, body map[string]any, _ llm.Request, _ registry.Resolved) error {
	body["messages"] = p.messages
	return nil
}

func (imageRecoveryPreparer) RequiresStreamingComplete() bool { return false }

func TestImageRecoveryFinalWirePreservation(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"provider_options", "transport_constants", "preparer"} {
		t.Run(source, func(t *testing.T) {
			jpegURI := recoveryURI(recoveryJPEG(t, nil))
			remoteURI := "https://example.invalid/preserve.png"
			var transparent bytes.Buffer
			if err := png.Encode(&transparent, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(transparent.Bytes())
			messages := []map[string]any{{"role": "user", "extension": uint64(9007199254740993), "content": []map[string]any{
				{"type": "text", "text": "opaque-user-sentinel"},
				{"type": "image_url", "image_url": map[string]any{"url": jpegURI, "detail": "high"}},
				{"type": "image_url", "image_url": map[string]any{"url": remoteURI}},
				{"type": "image_url", "image_url": map[string]any{"url": jpegURI}},
				{"type": "image_url", "image_url": map[string]any{"url": pngURI}},
			}}}
			messages = append(messages,
				map[string]any{"role": "assistant", "content": []any{}, "tool_calls": []any{
					map[string]any{"id": "call-sentinel", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}},
				}},
				map[string]any{"role": "tool", "tool_call_id": "call-sentinel", "content": "result-sentinel"},
			)
			before, _ := json.Marshal(messages)
			var bodies []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				decoder := json.NewDecoder(r.Body)
				decoder.UseNumber()
				if err := decoder.Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				if len(bodies) == 1 {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = io.WriteString(w, imageRejection)
					return
				}
				recoveryReply(w, false)
			}))
			defer srv.Close()
			res := liveRes(srv, nil)
			req := userReq("overridden-sentinel")
			ctx := t.Context()
			switch source {
			case "provider_options":
				req.ProviderOptions = map[string]any{registry.ProtocolOpenAIChat: map[string]any{"messages": messages}}
			case "transport_constants":
				res.Transport.Body = map[string]any{"messages": json.RawMessage(before)}
			case "preparer":
				res.Transport.Auth = registry.AuthOAuthOpenAICodex
				ctx = llm.WithAuthenticatorOverride(ctx, imageRecoveryPreparer{messages: messages})
			}
			if _, err := recoveryCall(ctx, &Protocol{Client: srv.Client()}, req, res, false); err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 2 {
				t.Fatalf("requests=%d", len(bodies))
			}
			first, second := recoveryImages(t, bodies[0]), recoveryImages(t, bodies[1])
			if !reflect.DeepEqual(first, []string{jpegURI, remoteURI, jpegURI, pngURI}) || len(second) != 4 || second[1] != remoteURI || second[0] == jpegURI || second[0] != second[2] || second[3] != pngURI {
				t.Fatal("image order, retention or repair changed")
			}
			// Normalize only repaired URLs, then compare the entire wire bodies.
			for _, raw := range bodies[1]["messages"].([]any) {
				parts, _ := raw.(map[string]any)["content"].([]any)
				for _, partRaw := range parts {
					part := partRaw.(map[string]any)
					if imageURL, ok := part["image_url"].(map[string]any); ok && imageURL["url"] != remoteURI && imageURL["url"] != pngURI {
						imageURL["url"] = jpegURI
					}
				}
			}
			if !reflect.DeepEqual(bodies[0], bodies[1]) {
				t.Fatal("unrelated request fields changed")
			}
			after, _ := json.Marshal(messages)
			if !bytes.Equal(before, after) {
				t.Fatal("overlaid caller messages changed")
			}
		})
	}
}

func TestImageRecoveryManagedThenProfileFree(t *testing.T) {
	t.Parallel()
	profile, err := os.ReadFile("../../internal/imagerepair/testdata/p3.icc")
	if err != nil {
		t.Fatal(err)
	}
	for _, statuses := range [][2]int{{400, 200}, {413, 200}, {400, 422}} {
		for _, stream := range []bool{false, true} {
			t.Run(http.StatusText(statuses[0])+"/"+http.StatusText(statuses[1])+map[bool]string{false: "/complete", true: "/stream"}[stream], func(t *testing.T) {
				originalPixels := recoveryNoisyJPEG(t)
				original := recoveryWithProfile(originalPixels, profile)
				var images []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					urls := recoveryImages(t, body)
					if len(urls) != 1 {
						t.Errorf("images=%d", len(urls))
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					images = append(images, urls[0])
					switch len(images) {
					case 1:
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, imageRejection)
					case 2:
						w.WriteHeader(statuses[0])
						_, _ = io.WriteString(w, imageRejection)
					case 3:
						if statuses[1] == 200 {
							recoveryReply(w, stream)
						} else {
							w.WriteHeader(statuses[1])
							_, _ = io.WriteString(w, imageRejection)
						}
					default:
						t.Error("unexpected resend")
						w.WriteHeader(http.StatusInternalServerError)
					}
				}))
				defer srv.Close()
				sink := &captureSink{}
				ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_image_stages")), sink)
				resp, err := recoveryCall(ctx, &Protocol{Client: srv.Client()}, recoveryRequest(original), liveRes(srv, nil), stream)
				if statuses[1] != 200 {
					var providerErr llm.Error
					if !errors.As(err, &providerErr) || providerErr.StatusCode() != statuses[1] {
						t.Fatalf("terminal error=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				records := sink.records()
				if len(records) != 3 {
					t.Fatalf("attempt records=%d", len(records))
				}
				for i, status := range []int{400, statuses[0], statuses[1]} {
					if records[i].Response == nil || records[i].Response.StatusCode == nil || *records[i].Response.StatusCode != status {
						t.Fatalf("attempt %d status record=%+v", i, records[i])
					}
				}
				if len(images) != 3 || images[0] != recoveryURI(original) || !strings.HasPrefix(images[1], "data:image/png;base64,") || images[2] != recoveryURI(originalPixels) {
					t.Fatal("recovery stages did not preserve original JPEG pixels")
				}
				if statuses[1] == 200 && (len(resp.Warnings) != 1 || resp.Warnings[0].Code != "image_color_unmanaged") {
					t.Fatalf("warnings=%+v", resp.Warnings)
				}
			})
		}
	}
}

func TestImageRecoveryLeavesAcceptedImagesUnchanged(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			data := recoveryJPEG(t, []byte("unsupported-profile"))
			var images [][]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				images = append(images, recoveryImages(t, body))
				recoveryReply(w, stream)
			}))
			defer srv.Close()
			resp, err := recoveryCall(t.Context(), &Protocol{Client: srv.Client()}, recoveryRequest(data), liveRes(srv, nil), stream)
			if err != nil || len(resp.Warnings) != 0 || !reflect.DeepEqual(images, [][]string{{recoveryURI(data)}}) {
				t.Fatalf("requests=%d warnings=%+v error=%v", len(images), resp.Warnings, err)
			}
		})
	}
}
