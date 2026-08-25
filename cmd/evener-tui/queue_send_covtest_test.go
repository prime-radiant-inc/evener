package tui

import (
	"os"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
)

// ---- buildAttachmentItems: error paths --------------------------------------

func TestCovBuildAttachmentItems_EmptyReturnsNil(t *testing.T) {
	items, err := buildAttachmentItems(nil)
	if err != nil {
		t.Fatalf("empty attachments error: %v", err)
	}
	if items != nil {
		t.Fatalf("empty attachments = %+v, want nil", items)
	}
}

func TestCovBuildAttachmentItems_NilAttachmentSkipped(t *testing.T) {
	items, err := buildAttachmentItems([]*clipboard.PastedImage{nil})
	if err != nil {
		t.Fatalf("nil attachment error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("nil attachment should produce 0 items: %+v", items)
	}
}

func TestCovBuildAttachmentItems_ReadError(t *testing.T) {
	att := &clipboard.PastedImage{Path: "/nonexistent/path/file.png"}
	_, err := buildAttachmentItems([]*clipboard.PastedImage{att})
	if err == nil {
		t.Fatalf("nonexistent file should produce error")
	}
}

func TestCovBuildAttachmentItems_DefaultMediaType(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/test.png"
	if err := os.WriteFile(path, []byte("fake png data"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	att := &clipboard.PastedImage{Path: path}
	items, err := buildAttachmentItems([]*clipboard.PastedImage{att})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items count = %d, want 1", len(items))
	}
	if items[0].MediaType != "image/png" {
		t.Fatalf("default media type = %q, want image/png", items[0].MediaType)
	}
}

func TestCovBuildAttachmentItems_ExplicitMediaType(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/test.jpg"
	if err := os.WriteFile(path, []byte("fake jpg"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	att := &clipboard.PastedImage{Path: path, MediaType: "image/jpeg"}
	items, err := buildAttachmentItems([]*clipboard.PastedImage{att})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(items) != 1 || items[0].MediaType != "image/jpeg" {
		t.Fatalf("media type = %q, want image/jpeg", items[0].MediaType)
	}
}

// ---- sendHubQueue: attachment error path (avoids needing a real client) ------

func TestCovSendHubQueue_WithAttachmentError(t *testing.T) {
	att := &clipboard.PastedImage{Path: "/nonexistent/file.png"}
	cmd := sendHubQueue(nil, appwire.Ref{SourceID: "local", ThreadID: "01"}, "text", "draft", []*clipboard.PastedImage{att})
	msg := cmd()
	if msg == nil {
		t.Fatalf("cmd should produce a msg")
	}
	qm, ok := msg.(hubQueueMsg)
	if !ok {
		t.Fatalf("msg type = %T, want hubQueueMsg", msg)
	}
	if qm.err == nil {
		t.Fatalf("should have error from bad attachment path")
	}
	if !qm.trackedAttachmentSubmit {
		t.Fatalf("trackedAttachmentSubmit should be true when attachments present")
	}
}

// ---- sendHubDrainAsSteer: attachment error and depth paths --------------------

func TestCovSendHubDrainAsSteer_WithAttachmentError(t *testing.T) {
	att := &clipboard.PastedImage{Path: "/nonexistent/file.png"}
	cmd := sendHubDrainAsSteer(nil, appwire.Ref{SourceID: "local", ThreadID: "01"}, "text", "draft", []*clipboard.PastedImage{att}, 0)
	msg := cmd()
	dm, ok := msg.(hubDrainAsSteerMsg)
	if !ok {
		t.Fatalf("msg type = %T, want hubDrainAsSteerMsg", msg)
	}
	if dm.err == nil {
		t.Fatalf("should have error from bad attachment path")
	}
}

func TestCovSendHubDrainAsSteer_WithPreQueueDepth(t *testing.T) {
	att := &clipboard.PastedImage{Path: "/nonexistent/file.png"}
	cmd := sendHubDrainAsSteer(nil, appwire.Ref{SourceID: "local", ThreadID: "01"}, "text", "draft", []*clipboard.PastedImage{att}, 0, 5)
	msg := cmd()
	dm, ok := msg.(hubDrainAsSteerMsg)
	if !ok {
		t.Fatalf("msg type = %T, want hubDrainAsSteerMsg", msg)
	}
	if dm.preQueueDepth != 5 {
		t.Fatalf("preQueueDepth = %d, want 5", dm.preQueueDepth)
	}
}

func TestCovSendHubDrainAsSteer_ExecutesCommandWithoutAttachments(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	client.Start(t.Context())

	gotMethod := make(chan string, 1)
	go func() {
		req := <-transport.Sent()
		gotMethod <- req.Request.Method
		transport.DeliverError(req.Request.ID, -32000, "drain rejected")
	}()

	ref := appwire.Ref{SourceID: "local", ThreadID: "01"}
	msg := sendHubDrainAsSteer(client, ref, "text", "draft", nil, 42, 5)()
	if method := <-gotMethod; method != appwire.MethodTurnDrainAsSteer {
		t.Fatalf("command sent method %q, want %q", method, appwire.MethodTurnDrainAsSteer)
	}
	dm, ok := msg.(hubDrainAsSteerMsg)
	if !ok {
		t.Fatalf("command message type = %T, want hubDrainAsSteerMsg", msg)
	}
	if dm.ref != ref.String() || dm.text != "text" || dm.draft != "draft" || dm.preQueueDepth != 5 {
		t.Fatalf("command result lost submission state: %+v", dm)
	}
	if dm.hadAttachment || dm.trackedAttachmentSubmit || len(dm.submittedAttachments) != 0 {
		t.Fatalf("attachment-free command reported attachments: %+v", dm)
	}
	if dm.err == nil || dm.err.Error() != "appwire turn/drainAsSteer: drain rejected" {
		t.Fatalf("command error = %v, want exact transport failure", dm.err)
	}
}

// ---- appendTextInput ---------------------------------------------------------

func TestCovAppendTextInputQueue(t *testing.T) {
	items, err := buildAttachmentItems(nil)
	if err != nil {
		t.Fatalf("buildAttachmentItems(nil) error: %v", err)
	}
	result := appendTextInput("hello", items)
	if len(result) != 1 || result[0].Text != "hello" {
		t.Fatalf("appendTextInput = %+v, want one text item with 'hello'", result)
	}
}

func TestCovAppendTextInputQueue_EmptyText(t *testing.T) {
	result := appendTextInput("  ", nil)
	if len(result) != 0 {
		t.Fatalf("appendTextInput with whitespace text = %+v, want empty", result)
	}
}
