# WebUI Inline Output Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show agent-produced images inline under the web UI tool row that produced them, without carrying generated image bytes in AppWire.

**Architecture:** Add lightweight `outputImages` descriptors to AppWire `commandExecution` items. Backend resolvers validate tool-result images, structured write target paths, and conservative shell path candidates; file-backed images load through a new `/doc/image` route, while transcript-backed image bytes load through the existing sha-addressed session image route extended to tool results. The frontend passes descriptors through AppWire event conversion and reuses the existing thumbnail, sheet, lightbox, and open-beside image UI.

**Tech Stack:** Go stdlib HTTP, Serf AppWire JSON structs, Serf event/projector packages, existing `fspaths.ResolveInRoot`, vanilla JavaScript renderer, JSDOM renderer tests.

## Global Constraints

- Default tests must be deterministic and must not require provider credentials, network access, quota, current model behavior, or ambient developer machine state.
- AppWire carries descriptors only; it must not carry generated file image bytes or base64 data.
- File-backed images must resolve inside the local session cwd using the same containment boundary as `/doc/file`.
- Supported image media in v1: PNG, JPEG, GIF, and WebP.
- SVG is excluded in v1.
- Shell inference is included in v1 but must be conservative and server-validated.
- Invalid candidates must not fail the tool row; omit the preview.
- Do not add a general artifact store.

---

## File structure

- `appwire/types.go`: define `OutputImage`; add `OutputImages []OutputImage` to `ThreadItem`.
- `appwire/types_test.go`: verify JSON round trip of `outputImages` and backwards compatibility.
- `agent/events/payloads.go`: add `OutputImages []appwire.OutputImage`-equivalent event payload data or a local event type to `ToolCallEndData`. To avoid an import cycle, define `events.OutputImage` with the same JSON shape and convert in projectors.
- `agent/session_model_call.go`: preserve `ExecResult.ImageData/ImageMediaType` into persisted `llm.ToolResultData` and `ToolCallEndData`.
- `internal/appprojector/appwire_projection.go`: project live `ToolCallEndData.OutputImages` to `ThreadItem.OutputImages`.
- `internal/appprojector/appwire_projection_test.go`: live projector tests.
- `internal/apptranscript/apptranscript.go`: project persisted tool-result image data to `ThreadItem.OutputImages` through a new resolver callback.
- `internal/apptranscript/apptranscript_test.go`: replay projection tests.
- `cmd/serf-hub/output_images.go`: new hub helper for output-image resolution, shell candidate extraction, media sniffing, descriptor creation, and route URL construction.
- `cmd/serf-hub/output_images_test.go`: resolver and candidate-extraction tests.
- `cmd/serf-hub/doc_serve.go`: add `handleDocImage` or delegate to helper in `output_images.go`.
- `cmd/serf-hub/doc_serve_test.go`: `/doc/image` security and content-type tests.
- `cmd/serf-hub/image_serve.go`: extend transcript sha lookup to include tool-result image bytes.
- `cmd/serf-hub/app_threadread.go`: pass hub-specific output-image resolver into transcript projection.
- `cmd/serf-hub/assets/appwire.js`: carry `outputImages` through commandExecution events.
- `cmd/serf-hub/assets/renderer.js`: render tool output images under the owning tool row.
- `cmd/serf-hub/assets/style.css`: add tool-output image wrapper styles by reusing existing user-image card rules.
- `cmd/serf-hub/jstest/test-renderer.js`: assert one and multiple tool output images render and lightbox opens.

---

### Task 1: AppWire and event descriptor plumbing

**Files:**
- Modify: `appwire/types.go`
- Modify: `appwire/types_test.go`
- Modify: `agent/events/payloads.go`
- Modify: `agent/session_model_call.go`
- Test: `appwire/types_test.go`
- Test: focused existing agent tests or new test near `agent/session_dod_definition_test.go`

**Interfaces:**
- Produces: `appwire.OutputImage` with fields `Source`, `Name`, `MediaType`, `Size`, `URL`, `SHA`, `Path`.
- Produces: `events.OutputImage` with the same JSON field names.
- Produces: `events.ToolCallEndData.OutputImages []events.OutputImage`.
- Consumes: `tool.ExecResult.ImageData`, `tool.ExecResult.ImageMediaType`, and `tool.ExecResult.ImagePurpose`.

- [ ] **Step 1: Add failing AppWire JSON round-trip test**

Add this test to `appwire/types_test.go`:

```go
func TestThreadItemOutputImagesJSONRoundTrip(t *testing.T) {
	item := ThreadItem{
		Type:     "commandExecution",
		ID:       "item_tool_1",
		ToolName: "shell",
		OutputImages: []OutputImage{{
			Source:    "shell-path",
			Name:      "out.png",
			MediaType: "image/png",
			Size:      67,
			URL:       "/doc/image?session=01ABC&path=out.png",
			SHA:       "abc123",
			Path:      "out.png",
		}},
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"outputImages"`) {
		t.Fatalf("encoded item missing outputImages: %s", data)
	}
	var got ThreadItem
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.OutputImages) != 1 {
		t.Fatalf("OutputImages length=%d, want 1", len(got.OutputImages))
	}
	img := got.OutputImages[0]
	if img.Source != "shell-path" || img.Name != "out.png" || img.MediaType != "image/png" || img.URL == "" || img.Path != "out.png" || img.Size != 67 {
		t.Fatalf("OutputImages[0]=%+v", img)
	}
}
```

If `types_test.go` lacks imports for `encoding/json` or `strings`, add them.

- [ ] **Step 2: Run the failing AppWire test**

Run:

```bash
go test ./appwire -run TestThreadItemOutputImagesJSONRoundTrip -count=1
```

Expected: FAIL because `OutputImage` or `ThreadItem.OutputImages` is undefined.

- [ ] **Step 3: Add AppWire descriptor type**

In `appwire/types.go`, near `ThreadItem`, add:

```go
type OutputImage struct {
	Source    string `json:"source"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Size      int64  `json:"size,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Path      string `json:"path,omitempty"`
}
```

Then add to `ThreadItem`:

```go
OutputImages []OutputImage `json:"outputImages,omitempty"`
```

Place it near `Output`, `Error`, and `Raw`, because it describes command output.

- [ ] **Step 4: Run the AppWire test again**

Run:

```bash
go test ./appwire -run TestThreadItemOutputImagesJSONRoundTrip -count=1
```

Expected: PASS.

- [ ] **Step 5: Add event descriptor type and ToolCallEndData field**

In `agent/events/payloads.go`, add below `ToolCallOutputDeltaData`:

```go
// OutputImage is a lightweight descriptor for an image produced by a tool. It
// carries placement and fetch metadata for dashboards; it never carries image
// bytes.
type OutputImage struct {
	Source    string `json:"source"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Size      int64  `json:"size,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Path      string `json:"path,omitempty"`
}
```

Add to `ToolCallEndData`:

```go
OutputImages []OutputImage `json:"output_images,omitempty"`
```

Use snake-case in events because surrounding event payload fields use snake-case.

- [ ] **Step 6: Preserve image bytes in tool result persistence**

Find the tool execution result handling in `agent/session_model_call.go` where `llm.ToolResultData` is constructed and `events.ToolCallEndData` is emitted. Update the `llm.ToolResultData` construction so it includes:

```go
ImageData:      res.ImageData,
ImageMediaType: res.ImageMediaType,
```

Do not add image bytes to `ToolCallEndData`. For Task 1, leave `OutputImages` empty; later hub/projector tasks add descriptors.

If the current code uses `llm.ToolResultNamed(...)`, replace that construction at this execution site with an explicit `llm.Message{Role: llm.RoleTool, ToolCallID: res.CallID, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{...}}}}` so image fields can be set.

- [ ] **Step 7: Add focused persistence test**

Add or update an agent test near existing tool image tests to assert a tool result persisted in session history has `ImageData` and `ImageMediaType` when `ExecResult` had them. Use a fake registered tool or existing `read_file` image fixture. The assertion should inspect the persisted `schema.Turn` and find a `llm.ContentToolResult` part whose `ToolResult.ImageMediaType == "image/png"` and `len(ImageData) > 0`.

- [ ] **Step 8: Run focused tests**

Run:

```bash
go test ./agent -run 'Test.*Image.*ToolResult|Test.*ReadFile.*Image' -count=1
go test ./appwire -run TestThreadItemOutputImagesJSONRoundTrip -count=1
```

Expected: PASS. If the regex does not match the new agent test, run the exact new test name.

- [ ] **Step 9: Commit**

```bash
git add appwire/types.go appwire/types_test.go agent/events/payloads.go agent/session_model_call.go agent/*_test.go
git commit -m "feat(appwire): add output image descriptors"
```

---

### Task 2: Hub output image resolver and `/doc/image`

**Files:**
- Create: `cmd/serf-hub/output_images.go`
- Create: `cmd/serf-hub/output_images_test.go`
- Modify: `cmd/serf-hub/doc_serve.go`
- Modify: `cmd/serf-hub/doc_serve_test.go`
- Modify: `cmd/serf-hub/web.go`

**Interfaces:**
- Produces: `supportedOutputImageMedia(data []byte, name string) (string, bool)`.
- Produces: `shellOutputImageCandidates(output string) []string`.
- Produces: `resolveOutputImageFile(sessionID, cwd, candidate, source string) (appwire.OutputImage, bool)` or equivalent method on `WebServer`.
- Produces: `handleDocImage(w http.ResponseWriter, r *http.Request)`.
- Consumes: `fspaths.ResolveInRoot(root, rel)`.

- [ ] **Step 1: Add failing candidate extraction tests**

Create `cmd/serf-hub/output_images_test.go` with:

```go
package main

import (
	"reflect"
	"testing"
)

func TestShellOutputImageCandidatesConservative(t *testing.T) {
	out := `wrote ./out.png
saved "screens/shot one.webp"
ignore https://example.com/nope.png
ignore notes.txt
absolute /tmp/project/chart.jpg
`
	got := shellOutputImageCandidates(out)
	want := []string{"./out.png", "screens/shot one.webp", "/tmp/project/chart.jpg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates=%#v, want %#v", got, want)
	}
}

func TestShellOutputImageCandidatesCapsResults(t *testing.T) {
	var out string
	for i := 0; i < 40; i++ {
		out += "img" + strconv.Itoa(i) + ".png\n"
	}
	got := shellOutputImageCandidates(out)
	if len(got) != 20 {
		t.Fatalf("candidate count=%d, want cap 20", len(got))
	}
}
```

Add `strconv` import for the second test.

- [ ] **Step 2: Run failing candidate tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestShellOutputImageCandidates' -count=1
```

Expected: FAIL because `shellOutputImageCandidates` is undefined.

- [ ] **Step 3: Implement candidate scanner and media sniffer**

Create `cmd/serf-hub/output_images.go`:

```go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

const outputImageMaxCandidates = 20
const outputImageMaxRendered = 8
const outputImageMaxBytes = 8 * 1024 * 1024

var outputImageExtRegexp = regexp.MustCompile(`(?i)(?:"([^"]+\.(?:png|jpe?g|gif|webp))"|'([^']+\.(?:png|jpe?g|gif|webp))'|([^\s"']+\.(?:png|jpe?g|gif|webp)))`)

func shellOutputImageCandidates(output string) []string {
	matches := outputImageExtRegexp.FindAllStringSubmatch(output, -1)
	out := make([]string, 0, min(len(matches), outputImageMaxCandidates))
	seen := map[string]struct{}{}
	for _, m := range matches {
		cand := ""
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				cand = strings.TrimSpace(m[i])
				break
			}
		}
		if cand == "" || strings.HasPrefix(strings.ToLower(cand), "http://") || strings.HasPrefix(strings.ToLower(cand), "https://") {
			continue
		}
		if _, ok := seen[cand]; ok {
			continue
		}
		seen[cand] = struct{}{}
		out = append(out, cand)
		if len(out) >= outputImageMaxCandidates {
			break
		}
	}
	return out
}

func supportedOutputImageMedia(data []byte, name string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	ct := http.DetectContentType(data)
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct, true
	}
	// http.DetectContentType can return octet-stream for small WebP samples; allow
	// only when the RIFF/WEBP signature is present.
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp", true
	}
	return "", false
}

func outputImageSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func outputImageDisplayName(path string) string {
	if base := filepath.Base(path); base != "." && base != string(filepath.Separator) {
		return base
	}
	return "image"
}
```

If this code needs Go version compatibility for `min`, replace `min(...)` with explicit capacity logic.

- [ ] **Step 4: Run candidate tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestShellOutputImageCandidates' -count=1
```

Expected: PASS.

- [ ] **Step 5: Add failing `/doc/image` tests**

Append to `cmd/serf-hub/doc_serve_test.go`:

```go
func docImageRequest(t *testing.T, web *WebServer, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/image?session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDocImageServesPNG(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "out.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docImageRequest(t, web, session, "out.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q, want image/png", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body=%x, want %x", rec.Body.Bytes(), png)
	}
}

func TestDocImageRejectsTraversalAndSVG(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	secret := filepath.Join(filepath.Dir(cwd), "secret.png")
	if err := os.WriteFile(secret, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := docImageRequest(t, web, session, "../"+filepath.Base(secret)); rec.Code == http.StatusOK {
		t.Fatalf("traversal image request got 200")
	}
	if err := os.WriteFile(filepath.Join(cwd, "x.svg"), []byte(`<svg></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := docImageRequest(t, web, session, "x.svg"); rec.Code == http.StatusOK {
		t.Fatalf("svg image request got 200")
	}
}
```

Add `bytes` import to `doc_serve_test.go`.

- [ ] **Step 6: Run failing `/doc/image` tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestDocImage' -count=1
```

Expected: FAIL because `/doc/image` is not routed.

- [ ] **Step 7: Implement `/doc/image` route**

In `cmd/serf-hub/web.go`, add:

```go
mux.HandleFunc("/doc/image", s.handleDocImage)
```

near `/doc/file`.

In `cmd/serf-hub/doc_serve.go`, add:

```go
func (s *WebServer) handleDocImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	session := canonicalRouteID(r.URL.Query().Get("session"))
	rel := r.URL.Query().Get("path")
	if session == "" || rel == "" {
		http.NotFound(w, r)
		return
	}
	cwd, ok := s.localSessionCWD(session)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		if errors.Is(err, fspaths.ErrPathEscapesRoot) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > outputImageMaxBytes {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mediaType, ok := supportedOutputImageMedia(data, filepath.Base(abs))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sha := outputImageSHA(data)
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Write(data) //nolint:errcheck
}
```

Ensure `doc_serve.go` imports still compile. It already imports `errors`, `net/http`, `os`, and `filepath`; add `fspaths` if not already imported in this file.

- [ ] **Step 8: Run route tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestDocImage|TestShellOutputImageCandidates' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/serf-hub/output_images.go cmd/serf-hub/output_images_test.go cmd/serf-hub/doc_serve.go cmd/serf-hub/doc_serve_test.go cmd/serf-hub/web.go
git commit -m "feat(hub): serve validated output images"
```

---

### Task 3: Project output image descriptors in live and replay paths

**Files:**
- Modify: `internal/appprojector/appwire_projection.go`
- Modify: `internal/appprojector/appwire_projection_test.go`
- Modify: `internal/apptranscript/apptranscript.go`
- Modify: `internal/apptranscript/apptranscript_test.go`
- Modify: `cmd/serf-hub/app_threadread.go`
- Modify: `cmd/serf-hub/image_serve.go`
- Test: `internal/appprojector/appwire_projection_test.go`
- Test: `internal/apptranscript/apptranscript_test.go`
- Test: `cmd/serf-hub/replay_fuzz_test.go` if replay helpers require field updates

**Interfaces:**
- Consumes: `events.ToolCallEndData.OutputImages`.
- Produces: conversion helper `projectOutputImages([]events.OutputImage) []appwire.OutputImage`.
- Produces: `apptranscript.OutputImageProjector` callback for `llm.ToolResultData`.
- Consumes: `llm.ToolResultData.ImageData/ImageMediaType`.

- [ ] **Step 1: Add failing live projector test**

In `internal/appprojector/appwire_projection_test.go`, add a test that projects `EventToolCallStart` followed by `EventToolCallEnd` with `OutputImages`. Assert the completed item has one `OutputImages` descriptor.

Use this core assertion:

```go
end := events.New(events.ToolCallEndData{
	ToolName: "shell",
	CallID:   "call_img",
	Output:   "wrote out.png",
	OutputImages: []events.OutputImage{{
		Source: "shell-path", Name: "out.png", MediaType: "image/png", URL: "/doc/image?session=01&path=out.png", Path: "out.png",
	}},
})
notes := p.Project(end)
item := completedItemFromNotificationsForTest(t, notes)
if len(item.OutputImages) != 1 || item.OutputImages[0].Name != "out.png" {
	t.Fatalf("OutputImages=%+v", item.OutputImages)
}
```

If no helper exists, inspect existing tests and extract the item from `notification.Params` the same way neighboring tests do.

- [ ] **Step 2: Run failing live projector test**

Run:

```bash
go test ./internal/appprojector -run Test.*OutputImages -count=1
```

Expected: FAIL because projector does not copy `OutputImages`.

- [ ] **Step 3: Implement live projection copy**

In `internal/appprojector/appwire_projection.go`, add:

```go
func projectOutputImages(images []events.OutputImage) []appwire.OutputImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]appwire.OutputImage, 0, len(images))
	for _, img := range images {
		if img.URL == "" && img.SHA == "" {
			continue
		}
		out = append(out, appwire.OutputImage{
			Source: img.Source, Name: img.Name, MediaType: img.MediaType,
			Size: img.Size, URL: img.URL, SHA: img.SHA, Path: img.Path,
		})
	}
	return out
}
```

In the `EventToolCallEnd` item construction, set:

```go
OutputImages: projectOutputImages(data.OutputImages),
```

- [ ] **Step 4: Run live projector test**

Run:

```bash
go test ./internal/appprojector -run Test.*OutputImages -count=1
```

Expected: PASS.

- [ ] **Step 5: Add failing apptranscript test for persisted tool-result images**

In `internal/apptranscript/apptranscript_test.go`, add a test that builds a `schema.TurnToolResults` with a `llm.ToolResultData{ImageData: png, ImageMediaType: "image/png"}` and passes an image projector callback that returns an `appwire.OutputImage` with `SHA` and `URL`. Assert the projected `commandExecution` item carries it.

Desired new interface:

```go
type OutputImageProjector func(result *llm.ToolResultData) []appwire.OutputImage
```

- [ ] **Step 6: Run failing apptranscript test**

Run:

```bash
go test ./internal/apptranscript -run TestProjectTurn.*OutputImages -count=1
```

Expected: FAIL because `ProjectTurn` has no output image projector.

- [ ] **Step 7: Extend apptranscript projection interface**

In `internal/apptranscript/apptranscript.go`:

1. Add:

```go
type OutputImageProjector func(result *llm.ToolResultData) []appwire.OutputImage
```

2. Change `ProjectTurn` signature to accept the new callback after `imageProjector`:

```go
func ProjectTurn(turnID string, turnIndex int, turn schema.Turn, toolNames map[string]string, imageProjector ImageProjector, outputImageProjector OutputImageProjector) (out []appwire.ThreadItem)
```

3. Preserve compatibility at call sites by passing `nil` initially. Inside `ProjectTurn`, when building a tool-result item, add:

```go
if outputImageProjector != nil {
	item.OutputImages = outputImageProjector(part.ToolResult)
}
```

4. Update all compile errors in callers/tests by adding the `nil` argument.

- [ ] **Step 8: Run apptranscript tests**

Run:

```bash
go test ./internal/apptranscript -run 'TestProjectTurn|Test.*OutputImages' -count=1
```

Expected: PASS.

- [ ] **Step 9: Extend transcript sha lookup for tool-result images**

In `cmd/serf-hub/image_serve.go`, update `findImageInTranscript` so it scans both:

- `ContentImage` user-input parts as it does now;
- `ContentToolResult` parts whose `ToolResult.ImageData` is non-empty.

When a matching tool-result sha is found, return `ToolResult.ImageData` and `ToolResult.ImageMediaType`.

Add or update a `cmd/serf-hub` test that writes a transcript entry with a tool result image and asserts `/s/<session>/images/<sha>` returns the bytes.

- [ ] **Step 10: Wire hub replay projector**

In `cmd/serf-hub/app_threadread.go`, update the `apptranscript.ProjectTurn` call to pass an `OutputImageProjector` that:

```go
func(result *llm.ToolResultData) []appwire.OutputImage {
	if result == nil || len(result.ImageData) == 0 {
		return nil
	}
	sha := imageSha(result.ImageData)
	mediaType := result.ImageMediaType
	if mediaType == "" { mediaType = "image/png" }
	return []appwire.OutputImage{{
		Source: "tool-result",
		Name: result.Name,
		MediaType: mediaType,
		Size: int64(len(result.ImageData)),
		SHA: sha,
		URL: "/s/" + url.PathEscape(sessionID) + "/images/" + sha,
	}}
}
```

Use the session id available in the surrounding `PastEntry`/`ReplayEntry` context. If `appItemsFromReplayTurn` lacks the session id, change its signature and update its callers.

- [ ] **Step 11: Run projection and hub tests**

Run:

```bash
go test ./internal/appprojector ./internal/apptranscript ./cmd/serf-hub -run 'OutputImages|SessionImage|Replay|AppItemsFromReplayTurn' -count=1
```

Expected: PASS.

- [ ] **Step 12: Commit**

```bash
git add internal/appprojector internal/apptranscript cmd/serf-hub/app_threadread.go cmd/serf-hub/image_serve.go cmd/serf-hub/*_test.go
git commit -m "feat(hub): project output image descriptors"
```

---

### Task 4: Discover file-backed descriptors from structured tools and shell output

**Files:**
- Modify: `cmd/serf-hub/output_images.go`
- Modify: `cmd/serf-hub/output_images_test.go`
- Modify: `server/appwire_server_test.go` or `internal/appprojector/appwire_projection_test.go` only if descriptor creation belongs outside hub
- Modify: `cmd/serf-hub/app_rpc.go` or hub replay/live bridge file if hub enriches AppWire responses

**Interfaces:**
- Consumes: tool name, arguments JSON, output text, session id, cwd.
- Produces: `outputImagesForToolCall(sessionID, cwd, toolName, argumentsJSON, output string) []appwire.OutputImage`.
- Uses: `shellOutputImageCandidates`, `resolveOutputImageFile`.

- [ ] **Step 1: Add failing resolver tests for structured writes**

In `cmd/serf-hub/output_images_test.go`, add tests using `t.TempDir()` and a PNG file under cwd. Assert:

```go
imgs := outputImagesForToolCall("01DOC", cwd, "write_file", `{"file_path":"out.png"}`, "wrote")
if len(imgs) != 1 || imgs[0].Source != "written-file" || imgs[0].URL == "" || imgs[0].Path != "out.png" { t.Fatalf(...) }
```

Add negative tests for `../outside.png` and SVG.

- [ ] **Step 2: Add failing resolver tests for shell output**

Add:

```go
imgs := outputImagesForToolCall("01DOC", cwd, "shell", `{}`, "created ./plot.png\n")
if len(imgs) != 1 || imgs[0].Source != "shell-path" { t.Fatalf(...) }
```

Also assert only 8 images are returned when output lists more than 8 valid files.

- [ ] **Step 3: Run failing resolver tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestOutputImagesForToolCall' -count=1
```

Expected: FAIL because the helper is undefined.

- [ ] **Step 4: Implement resolver helpers**

In `cmd/serf-hub/output_images.go`, add:

```go
func outputImagesForToolCall(sessionID, cwd, toolName, argumentsJSON, output string) []appwire.OutputImage {
	var candidates []struct{ path, source string }
	var args map[string]any
	_ = json.Unmarshal([]byte(argumentsJSON), &args)
	addArgPath := func(key string) {
		if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
			candidates = append(candidates, struct{ path, source string }{v, "written-file"})
		}
	}
	switch toolName {
	case "write_file", "edit_file":
		addArgPath("file_path")
		addArgPath("path")
	case "apply_patch":
		for _, p := range shellOutputImageCandidates(output) {
			candidates = append(candidates, struct{ path, source string }{p, "written-file"})
		}
	case "shell", "exec_command":
		for _, p := range shellOutputImageCandidates(output) {
			candidates = append(candidates, struct{ path, source string }{p, "shell-path"})
		}
	}
	out := make([]appwire.OutputImage, 0, min(len(candidates), outputImageMaxRendered))
	seen := map[string]struct{}{}
	for _, c := range candidates {
		img, ok := resolveOutputImageFile(sessionID, cwd, c.path, c.source)
		if !ok || img.URL == "" {
			continue
		}
		key := img.URL
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, img)
		if len(out) >= outputImageMaxRendered {
			break
		}
	}
	return out
}
```

Implement `resolveOutputImageFile` to use `fspaths.ResolveInRoot`, stat size, read bytes only after size cap, call `supportedOutputImageMedia`, compute sha, and return:

```go
appwire.OutputImage{
	Source: source,
	Name: outputImageDisplayName(rel),
	MediaType: mediaType,
	Size: info.Size(),
	URL: "/doc/image?session=" + url.QueryEscape(sessionID) + "&path=" + url.QueryEscape(rel),
	SHA: outputImageSHA(data),
	Path: rel,
}
```

For absolute candidate paths under cwd, convert to cwd-relative before building the URL.

- [ ] **Step 5: Run resolver tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestOutputImagesForToolCall|TestShellOutputImageCandidates|TestDocImage' -count=1
```

Expected: PASS.

- [ ] **Step 6: Integrate descriptor discovery into hub projection path**

Find the hub path that turns live AppWire `commandExecution` items or replay tool results into responses. Add `outputImagesForToolCall` at the point where session id, cwd, tool name, arguments JSON, and output are all available.

Expected behavior:

- For live local sessions, completed tool items get file-backed descriptors for `write_file`, `edit_file`, `apply_patch`, `shell`, and `exec_command`.
- For replay/past sessions, completed tool items get the same descriptors if the files still exist under cwd.
- Tool-result byte descriptors from Task 3 remain present and are appended before file-backed descriptors.

If live projection in `internal/appprojector` cannot know cwd, do not put file-backed discovery there. Enrich in `cmd/serf-hub` where local session cwd is available.

- [ ] **Step 7: Add integration test for replay descriptor enrichment**

Add a `cmd/serf-hub` test that creates a past session with cwd, writes `plot.png`, creates a replay tool result for shell output `created plot.png`, reads the thread through the hub AppWire/read path, and asserts the `commandExecution` item has one `OutputImages` descriptor with `/doc/image` URL.

- [ ] **Step 8: Run hub integration tests**

Run:

```bash
go test ./cmd/serf-hub -run 'OutputImages|ThreadRead|AppWire' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/serf-hub
git commit -m "feat(hub): discover file-backed output images"
```

---

### Task 5: Frontend rendering for tool output images

**Files:**
- Modify: `cmd/serf-hub/assets/appwire.js`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-renderer.js` or add `cmd/serf-hub/jstest/test-renderer-output-images.js`

**Interfaces:**
- Consumes: `item.outputImages` from AppWire commandExecution items.
- Produces renderer payload field: `output_images` or `outputImages`.
- Produces DOM: `.tool-output-images`, containing existing `.user-image-card` or `.user-image-sheet` descendants.

- [ ] **Step 1: Add failing JS renderer test**

Create `cmd/serf-hub/jstest/test-renderer-output-images.js` based on `test-renderer.js`. Use a minimal event stream:

```js
const events = [
  ["SESSION_START", { model: "test", profile: "test", restored: true, session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "call_img", tool_name: "shell", arguments_json: "{}" }],
  ["TOOL_CALL_END", { call_id: "call_img", tool_name: "shell", output: "created out.png", output_images: [
    { source: "shell-path", name: "out.png", mediaType: "image/png", url: "/doc/image?session=01TEST&path=out.png", path: "out.png" }
  ]}],
];
```

Assert:

```js
const tool = conv.querySelector(".tool-call.shell");
pass(!!tool, "shell tool row rendered");
const wrap = tool.querySelector(".tool-output-images");
pass(!!wrap, "tool output image wrapper rendered");
const img = wrap && wrap.querySelector("img.user-image-thumb");
pass(img && img.getAttribute("src") === "/doc/image?session=01TEST&path=out.png", "tool image src wrong");
```

Also dispatch a click on the thumbnail card and assert `.image-lightbox` appears.

- [ ] **Step 2: Run failing JS test**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-output-images.js
```

Expected: FAIL because output images are ignored.

- [ ] **Step 3: Pass descriptors through `appwire.js`**

In commandExecution event creation in `cmd/serf-hub/assets/appwire.js`, include:

```js
output_images: item.outputImages || item.output_images || []
```

in both `TOOL_CALL_END` construction paths: cold item conversion and live completed-item reconciliation.

- [ ] **Step 4: Render images in `renderer.js`**

In `finalizeToolCall(data)`, after `if (m.renderer.bodyEnd) ...`, add:

```js
this.renderToolOutputImages(m, data.output_images || data.outputImages || []);
```

Add helper methods near existing image helpers:

```js
renderToolOutputImages(state, images) {
  if (!state || !state.el || !Array.isArray(images) || images.length === 0) return;
  const resolved = [];
  for (const img of images) {
    if (!img) continue;
    const src = img.url || img.URL || "";
    if (!src || src.charAt(0) !== "/") continue;
    resolved.push({ src, name: img.name || img.path || "image" });
  }
  if (resolved.length === 0) return;
  let wrap = state.el.querySelector(".tool-output-images");
  if (wrap) wrap.remove();
  wrap = document.createElement("div");
  wrap.className = "tool-output-images tool-body";
  if (resolved.length === 1) {
    wrap.appendChild(this.buildSingleImageCard(resolved, 0));
  } else {
    wrap.appendChild(this.buildImageSheet(resolved));
  }
  state.el.appendChild(wrap);
}
```

Keep URL acceptance same-origin relative for v1. Do not render arbitrary external URLs from descriptors.

- [ ] **Step 5: Add CSS**

In `cmd/serf-hub/assets/style.css`, near tool body or image styles, add:

```css
.tool-output-images {
  width: 100%;
  margin-top: var(--space-2);
  padding: var(--space-2) 0 0 calc(var(--space-5));
  background: transparent;
  border: 0;
}
.tool-output-images .user-image-card,
.tool-output-images .user-image-sheet {
  max-width: min(520px, 100%);
}
.tool-output-images .user-image-sheet {
  margin-bottom: 0;
}
```

Adjust spacing if existing tool body layout requires a different left indent.

- [ ] **Step 6: Run JS tests**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-output-images.js
node cmd/serf-hub/jstest/test-renderer.js
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/assets/appwire.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-renderer-output-images.js
git commit -m "feat(web): render tool output images inline"
```

---

### Task 6: End-to-end scenario tests and final verification

**Files:**
- Modify/Create: `test/scenarios` docs or tests if scenario-card harness exists.
- Modify: focused Go/JS tests as needed from failures.
- No product code unless fixing defects found by verification.

**Interfaces:**
- Consumes all prior tasks.
- Produces final verified implementation matching all E2E scenario cards in the spec.

- [ ] **Step 1: Map E2E scenario cards to tests**

Create or update the scenario documentation/test entry so these cards are represented exactly:

- `read-image-tool-result-inline`
- `written-image-inline-after-reload`
- `shell-generated-image-path-inline`
- `unsafe-image-path-ignored`
- `output-image-lightbox-and-pane`

If the existing repo only documents scenario cards, update the appropriate markdown or generated test fixture. If it has executable scenario tests, add deterministic local tests using temp files and scripted provider behavior.

- [ ] **Step 2: Run targeted Go tests**

Run:

```bash
go test ./appwire ./agent ./internal/appprojector ./internal/apptranscript ./cmd/serf-hub -run 'OutputImages|DocImage|SessionImage|Image.*ToolResult|ThreadRead|AppWire' -count=1
```

Expected: PASS.

- [ ] **Step 3: Run targeted JS tests**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-output-images.js
node cmd/serf-hub/jstest/test-renderer.js
node cmd/serf-hub/jstest/test-appwire-jsonrpc-lite.js
```

Expected: PASS.

- [ ] **Step 4: Run package tests for changed packages**

Run:

```bash
go test ./appwire ./agent ./internal/appprojector ./internal/apptranscript ./cmd/serf-hub -count=1
```

Expected: PASS.

- [ ] **Step 5: Run lint/format checks for touched Go code**

Run:

```bash
gofmt -w appwire/types.go agent/events/payloads.go agent/session_model_call.go internal/appprojector/appwire_projection.go internal/apptranscript/apptranscript.go cmd/serf-hub/*.go appwire/*_test.go agent/*_test.go internal/appprojector/*_test.go internal/apptranscript/*_test.go cmd/serf-hub/*_test.go
go test ./appwire ./agent ./internal/appprojector ./internal/apptranscript ./cmd/serf-hub -count=1
```

Expected: no formatting diff remains; tests PASS.

- [ ] **Step 6: Inspect git diff**

Run:

```bash
git status --short
git diff --stat
git diff --check
```

Expected: only intended tracked files changed; `git diff --check` prints nothing and exits 0.

- [ ] **Step 7: Commit final fixes or verification marker**

If Step 1 added scenario docs/tests or Steps 2-6 required fixes, commit them:

```bash
git status --short
git add test/scenarios/scenario_docs_test.go test/scenarios/*.md cmd/serf-hub/*_test.go cmd/serf-hub/assets/*.js cmd/serf-hub/assets/style.css appwire/*.go agent/*.go internal/appprojector/*.go internal/apptranscript/*.go
git commit -m "test(web): cover inline output image scenarios"
```

If there are no changes after verification, do not create an empty commit.

- [ ] **Step 8: Final report**

Report:

- commits created;
- tests run and results;
- any skipped scenario and why;
- final `git status --short`.
