package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
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

func TestShellOutputImageCandidatesRejectsEmbeddedURLs(t *testing.T) {
	out := `local before ./before.png
html src=https://example.com/from-attr.png
paren (https://example.com/from-paren.png)
markdown ![alt](https://example.com/from-markdown.png)
local after ./after.webp
`
	got := shellOutputImageCandidates(out)
	want := []string{"./before.png", "./after.webp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates=%#v, want %#v", got, want)
	}
}

func TestShellOutputImageCandidatesCapsResults(t *testing.T) {
	var out strings.Builder
	for i := range 40 {
		out.WriteString("img")
		out.WriteString(strconv.Itoa(i))
		out.WriteString(".png\n")
	}
	got := shellOutputImageCandidates(out.String())
	if len(got) != 20 {
		t.Fatalf("candidate count=%d, want cap 20", len(got))
	}
}

func TestSupportedOutputImageMediaAcceptsV1FormatsAndRejectsSVG(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "out.png", data: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, want: "image/png"},
		{name: "out.jpg", data: []byte{0xff, 0xd8, 0xff, 0xdb}, want: "image/jpeg"},
		{name: "out.gif", data: []byte("GIF89a"), want: "image/gif"},
		{name: "out.webp", data: []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), want: "image/webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := supportedOutputImageMedia(tt.data, tt.name)
			if !ok || got != tt.want {
				t.Fatalf("supportedOutputImageMedia(%s)=(%q,%v), want (%q,true)", tt.name, got, ok, tt.want)
			}
		})
	}

	if got, ok := supportedOutputImageMedia([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "out.svg"); ok {
		t.Fatalf("supportedOutputImageMedia(svg)=(%q,true), want rejected", got)
	}
}

func TestResolveOutputImageFileBuildsDescriptor(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "out.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := resolveOutputImageFile("01DOC", cwd, "out.png", "shell-path")
	if !ok {
		t.Fatalf("resolveOutputImageFile rejected valid PNG")
	}
	h := sha256.Sum256(png)
	wantSHA := hex.EncodeToString(h[:])
	if got.Source != "shell-path" || got.Name != "out.png" || got.MediaType != "image/png" ||
		got.Size != int64(len(png)) || got.URL != "/doc/image?session=01DOC&path=out.png" ||
		got.SHA != wantSHA || got.Path != "out.png" {
		t.Fatalf("descriptor=%+v, want source/name/media/size/url/sha/path for out.png", got)
	}
}

func TestResolveOutputImageFileRejectsInvalidCandidates(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(filepath.Dir(cwd), "outside.png")
	if err := os.WriteFile(outside, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveOutputImageFile("01DOC", cwd, "../"+filepath.Base(outside), "shell-path"); ok {
		t.Fatalf("resolveOutputImageFile accepted traversal candidate")
	}
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveOutputImageFile("01DOC", cwd, "notes.txt", "shell-path"); ok {
		t.Fatalf("resolveOutputImageFile accepted non-image candidate")
	}
}

func TestOutputImagesForToolCallStructuredWriteFile(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "out.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "write_file", `{"file_path":"out.png"}`, "wrote")
	if len(imgs) != 1 || imgs[0].Source != "written-file" || imgs[0].URL == "" || imgs[0].Path != "out.png" {
		t.Fatalf("outputImagesForToolCall write_file=%+v, want one written-file out.png descriptor", imgs)
	}
}

func TestOutputImagesForToolCallEditFileStructuredWrite(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "edited.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "edit_file", `{"file_path":"edited.png"}`, "updated")
	if len(imgs) != 1 || imgs[0].Source != "written-file" || imgs[0].Path != "edited.png" || imgs[0].URL == "" {
		t.Fatalf("outputImagesForToolCall edit_file=%+v, want one written-file edited.png descriptor", imgs)
	}
}

func TestOutputImagesForToolCallReadFileStructuredRead(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "screenshot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "read_file", `{"file_path":"screenshot.png"}`,
		"[image: png, 12 bytes, base64 data follows]")
	if len(imgs) != 1 || imgs[0].Source != "read-file" || imgs[0].Path != "screenshot.png" || imgs[0].URL == "" {
		t.Fatalf("outputImagesForToolCall read_file=%+v, want one read-file screenshot.png descriptor", imgs)
	}
}

func TestOutputImagesForToolCallApplyPatchOutputPath(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "patch.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "apply_patch", `{}`, "created patch.png")
	if len(imgs) != 1 || imgs[0].Source != "written-file" || imgs[0].Path != "patch.png" || imgs[0].URL == "" {
		t.Fatalf("outputImagesForToolCall apply_patch=%+v, want one written-file patch.png descriptor", imgs)
	}
}

func TestOutputImagesForToolCallDedupesDuplicateCandidates(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "dup.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "shell", `{}`, "created dup.png\nopened ./dup.png\n")
	if len(imgs) != 1 || imgs[0].Path != "dup.png" {
		t.Fatalf("outputImagesForToolCall duplicate shell paths=%+v, want one dup.png descriptor", imgs)
	}
}

func TestEnrichThreadFileBackedOutputImagesIsIdempotent(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "dup.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	thread := appwire.Thread{
		ID:        "01DOC",
		SessionID: "01DOC",
		CWD:       cwd,
		Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{
			Type:     "commandExecution",
			ToolName: "shell",
			Output:   "created dup.png",
			Status:   appwire.TurnStatusCompleted,
		}}}},
	}

	once := enrichThreadFileBackedOutputImages(thread)
	twice := enrichThreadFileBackedOutputImages(once)
	imgs := twice.Turns[0].Items[0].OutputImages
	if len(imgs) != 1 || imgs[0].Path != "dup.png" {
		t.Fatalf("OutputImages after repeated enrichment=%+v, want one dup.png descriptor", imgs)
	}
}

// TestAppendOutputImagesUniqueDedupesBySHAAcrossDifferentURLs covers the
// interaction read_file's file-backed descriptor (source "read-file", URL
// via /doc/image) creates with the pre-existing tool-result descriptor a past
// thread read already attaches for the exact same call (source "tool-result",
// URL via the sha-addressed /s/.../images/ route, app_threadread.go's
// projectReplayOutputImages) - same underlying bytes, same sha, two different
// URLs. outputImageDescriptorKey must treat that as one image, not two.
func TestAppendOutputImagesUniqueDedupesBySHAAcrossDifferentURLs(t *testing.T) {
	existing := []appwire.OutputImage{{Source: "tool-result", SHA: "abc123", URL: "/s/01DOC/images/abc123"}}
	extra := []appwire.OutputImage{{Source: "read-file", SHA: "abc123", URL: "/doc/image?session=01DOC&path=shot.png"}}

	got := appendOutputImagesUnique(existing, extra)
	if len(got) != 1 {
		t.Fatalf("appendOutputImagesUnique(matching SHA, different URLs)=%+v, want deduped to one entry", got)
	}
}

func TestOutputImagesForToolCallRejectsUnsafeAbsoluteOutsidePath(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(filepath.Dir(cwd), "outside.png")
	if err := os.WriteFile(outside, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "write_file", `{"file_path":`+strconv.Quote(outside)+`}`, "wrote")
	if len(imgs) != 0 {
		t.Fatalf("outputImagesForToolCall accepted unsafe absolute outside path: %+v", imgs)
	}
}

func TestOutputImagesForToolCallOmitsMissingAndNonImageCandidates(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.png"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "shell", `{}`, "created missing.png\ncreated notes.png\n")
	if len(imgs) != 0 {
		t.Fatalf("outputImagesForToolCall returned invalid candidates: %+v", imgs)
	}
}

// TestEnrichThreadFileBackedOutputImagesDoesNotDuplicateAnAlreadyProjectedReadFileImage
// simulates the real past-thread-read pipeline order (app_threadread.go's
// pastEntryLatestTurns already ran projectReplayOutputImages before
// reconcileAndEnrichPastThread's enrichThreadFileBackedOutputImages runs):
// a read_file item that already carries a tool-result-sourced OutputImage
// for the file it read must not gain a second, file-backed entry for the
// same bytes.
func TestEnrichThreadFileBackedOutputImagesDoesNotDuplicateAnAlreadyProjectedReadFileImage(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := outputImageSHA(png)
	thread := appwire.Thread{
		ID:        "01DOC",
		SessionID: "01DOC",
		CWD:       cwd,
		Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{
			Type:          "commandExecution",
			ToolName:      "read_file",
			ArgumentsJSON: `{"file_path":"shot.png"}`,
			Output:        "[image: png, 12 bytes, base64 data follows]",
			Status:        appwire.TurnStatusCompleted,
			OutputImages: []appwire.OutputImage{{
				Source: "tool-result", MediaType: "image/png", Size: int64(len(png)),
				SHA: sha, URL: "/s/01DOC/images/" + sha,
			}},
		}}}},
	}

	got := enrichThreadFileBackedOutputImages(thread)
	imgs := got.Turns[0].Items[0].OutputImages
	if len(imgs) != 1 {
		t.Fatalf("OutputImages=%+v, want the pre-existing tool-result entry left alone, not duplicated", imgs)
	}
}

func TestEnrichThreadFileBackedOutputImagesIsPageLocalForArguments(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	thread := appwire.Thread{
		ID:        "01DOC",
		SessionID: "01DOC",
		CWD:       cwd,
		Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{
			Type:     "commandExecution",
			ToolName: "write_file",
			CallID:   "call_write",
			Output:   "wrote file",
			Status:   appwire.TurnStatusCompleted,
		}}}},
	}

	got := enrichThreadFileBackedOutputImages(thread)
	if imgs := got.Turns[0].Items[0].OutputImages; len(imgs) != 0 {
		t.Fatalf("page without write_file arguments produced descriptors: %+v", imgs)
	}
}

func TestOutputImagesForToolCallRejectsInvalidStructuredWriteCandidates(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(filepath.Dir(cwd), "outside.png")
	if err := os.WriteFile(outside, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "out.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}

	if imgs := outputImagesForToolCall("01DOC", cwd, "write_file", `{"file_path":"../outside.png"}`, "wrote"); len(imgs) != 0 {
		t.Fatalf("outputImagesForToolCall accepted outside path: %+v", imgs)
	}
	if imgs := outputImagesForToolCall("01DOC", cwd, "write_file", `{"file_path":"out.svg"}`, "wrote"); len(imgs) != 0 {
		t.Fatalf("outputImagesForToolCall accepted SVG: %+v", imgs)
	}
}

func TestOutputImagesForToolCallShellOutput(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "shell", `{}`, "created ./plot.png\n")
	if len(imgs) != 1 || imgs[0].Source != "shell-path" || imgs[0].Path != "plot.png" || imgs[0].URL == "" {
		t.Fatalf("outputImagesForToolCall shell=%+v, want one shell-path plot.png descriptor", imgs)
	}
}

func TestOutputImagesForToolCallCapsRenderedShellImages(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	var output strings.Builder
	for i := range outputImageMaxRendered + 3 {
		name := "plot" + strconv.Itoa(i) + ".png"
		if err := os.WriteFile(filepath.Join(cwd, name), png, 0o644); err != nil {
			t.Fatal(err)
		}
		output.WriteString("created ")
		output.WriteString(name)
		output.WriteString("\n")
	}

	imgs := outputImagesForToolCall("01DOC", cwd, "exec_command", `{}`, output.String())
	if len(imgs) != outputImageMaxRendered {
		t.Fatalf("outputImagesForToolCall returned %d images, want cap %d: %+v", len(imgs), outputImageMaxRendered, imgs)
	}
}

func TestEnrichOutputImageNotificationUsesStartedArgumentsForCompletedItem(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	argsByCallID := map[string]string{}
	started := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"turnId": "turn_1",
		"item": appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            "item_write",
			ToolName:      "write_file",
			CallID:        "call_write",
			ArgumentsJSON: `{"file_path":"plot.png"}`,
			Status:        appwire.TurnStatusInProgress,
		},
	}).Notification
	_ = enrichOutputImageNotification("01DOC", cwd, argsByCallID, *started)
	completed := appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
		"turnId": "turn_1",
		"item": appwire.ThreadItem{
			Type:     "commandExecution",
			ID:       "item_write",
			ToolName: "write_file",
			CallID:   "call_write",
			Output:   "wrote",
			Status:   appwire.TurnStatusCompleted,
		},
	}).Notification

	got := enrichOutputImageNotification("01DOC", cwd, argsByCallID, *completed)
	var params struct {
		Item appwire.ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(got.Params, &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Item.OutputImages) != 1 || params.Item.OutputImages[0].Source != "written-file" || params.Item.OutputImages[0].Path != "plot.png" {
		t.Fatalf("OutputImages=%+v, want written-file plot.png descriptor", params.Item.OutputImages)
	}
}

// enrichedItem runs a live item/completed notification through the relay's
// output-image enrichment and returns the item the browser would receive.
func enrichedItem(t *testing.T, sessionID, cwd string, item appwire.ThreadItem) appwire.ThreadItem {
	t.Helper()
	params, err := json.Marshal(map[string]any{"threadId": sessionID, "turnId": "turn_1", "item": item})
	if err != nil {
		t.Fatal(err)
	}
	got := enrichOutputImageNotification(sessionID, cwd, map[string]string{}, appwire.Notification{
		Method: appwire.NotifyItemCompleted,
		Params: params,
	})
	var decoded struct {
		Item appwire.ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(got.Params, &decoded); err != nil {
		t.Fatalf("unmarshal enriched params: %v", err)
	}
	return decoded.Item
}

// TestEnrichOutputImageNotificationStampsTheSHARouteOnALiveToolResult is the
// live half of the sha-addressed path: the daemon publishes a descriptor that
// names the bytes but no route, and the relay is where the route it can be
// fetched from gets attached.
func TestEnrichOutputImageNotificationStampsTheSHARouteOnALiveToolResult(t *testing.T) {
	sha := strings.Repeat("d", 64)
	item := enrichedItem(t, "02wMz5Txv733WHFsVy66SR", t.TempDir(), appwire.ThreadItem{
		Type: "commandExecution", ToolName: "screenshot", CallID: "call_shot",
		Status:       appwire.TurnStatusCompleted,
		OutputImages: []appwire.OutputImage{{Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 12, SHA: sha}},
	})
	if len(item.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want the one tool-result descriptor", item.OutputImages)
	}
	if item.OutputImages[0].URL != "/s/02wMz5Txv733WHFsVy66SR/images/"+sha {
		t.Fatalf("OutputImages[0].URL=%q, want the sha route", item.OutputImages[0].URL)
	}
}

// TestEnrichOutputImageNotificationStampsTheSHARouteWithoutACWD pins that the
// sha stamp does not depend on knowing the session's working directory — only
// the file-backed mechanism needs that, and a session with no recorded cwd
// still has servable tool-result bytes.
func TestEnrichOutputImageNotificationStampsTheSHARouteWithoutACWD(t *testing.T) {
	sha := strings.Repeat("e", 64)
	item := enrichedItem(t, "02wMz5Txv733WHFsVy66SR", "", appwire.ThreadItem{
		Type: "commandExecution", ToolName: "screenshot", CallID: "call_shot",
		Status:       appwire.TurnStatusCompleted,
		OutputImages: []appwire.OutputImage{{Source: "tool-result", SHA: sha}},
	})
	if len(item.OutputImages) != 1 || item.OutputImages[0].URL != "/s/02wMz5Txv733WHFsVy66SR/images/"+sha {
		t.Fatalf("OutputImages=%+v, want the sha route stamped with no cwd", item.OutputImages)
	}
}

// TestEnrichOutputImageNotificationKeepsOneEntryWhenBothMechanismsSeeTheSameFile
// covers a read_file of an image, where the sha-addressed tool-result
// descriptor and the file-backed /doc/image descriptor describe the same bytes.
// appendOutputImagesUnique's sha-first key collapses them; the tool-result
// route wins because it is already in hand.
func TestEnrichOutputImageNotificationKeepsOneEntryWhenBothMechanismsSeeTheSameFile(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := imageSha(png)
	item := enrichedItem(t, "02wMz5Txv733WHFsVy66SR", cwd, appwire.ThreadItem{
		Type: "commandExecution", ToolName: "read_file", CallID: "call_read",
		ArgumentsJSON: `{"file_path":"shot.png"}`,
		Status:        appwire.TurnStatusCompleted,
		OutputImages:  []appwire.OutputImage{{Source: "tool-result", MediaType: "image/png", Size: int64(len(png)), SHA: sha}},
	})
	if len(item.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want the two mechanisms deduped to one entry", item.OutputImages)
	}
	if item.OutputImages[0].Source != "tool-result" || item.OutputImages[0].URL != "/s/02wMz5Txv733WHFsVy66SR/images/"+sha {
		t.Fatalf("OutputImages[0]=%+v, want the sha-routed tool-result descriptor", item.OutputImages[0])
	}
}
