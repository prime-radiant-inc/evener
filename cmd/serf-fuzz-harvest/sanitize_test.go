package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A planted OpenAI key in a normal string value must be scrubbed away by
// construction: the detector flags it in the raw input (red before), and it is
// absent from the sanitized output, which the gate passes (green after).
func TestShapeScrubStripsPlantedSecret(t *testing.T) {
	const key = "sk-proj-abcdefABCDEF0123456789ghijklMNOP"
	raw := []byte(`{"messages":[{"role":"user","content":"my key is ` + key + `"}]}`)

	if detectSecret(raw, false) == "" {
		t.Fatal("detector did not flag the planted secret in raw input (should be red before)")
	}

	s := &Sanitizer{}
	out, err := s.Process(raw, false)
	if err != nil {
		t.Fatalf("Process returned error on scrubbable secret: %v", err)
	}
	if bytes.Contains(out, []byte("sk-")) || bytes.Contains(out, []byte(key)) {
		t.Fatalf("planted secret survived shape-scrub: %s", out)
	}
	if detectSecret(out, false) != "" {
		t.Fatalf("gate not green after scrub: %s", out)
	}
	// Structure survives: role enum kept, content value bucketed.
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("scrubbed output is not valid JSON: %v", err)
	}
}

// A secret hiding in an enum-allowlisted value survives shape-scrub (enum values
// are kept verbatim), so the abort gate must catch it: Process drops the seed
// and returns SecretLeakError, which the harvester turns into a non-zero exit.
func TestAbortGateCatchesSecretInEnumValue(t *testing.T) {
	const key = "sk-ant-api03-abcdefABCDEF0123456789ghijklmnop"
	raw := []byte(`{"type":"` + key + `","data":"hello"}`)

	s := &Sanitizer{}
	out, err := s.Process(raw, false)
	if err == nil {
		t.Fatalf("Process did NOT abort on a secret in an enum value; output=%s", out)
	}
	var leak *SecretLeakError
	if !errors.As(err, &leak) {
		t.Fatalf("expected *SecretLeakError, got %T: %v", err, err)
	}
	if out != nil {
		t.Fatalf("leaking seed must be dropped (nil bytes), got %s", out)
	}
}

// Under --keep-values a known secret is redacted in place (values otherwise
// preserved), so the output is real-but-clean and the gate passes.
func TestKeepValuesRedactsKnownSecret(t *testing.T) {
	const key = "AIzaSyA1234567890123456789012345678901234"
	raw := []byte(`{"note":"token ` + key + ` here","count":42}`)

	s := &Sanitizer{keepValues: true}
	out, err := s.Process(raw, false)
	if err != nil {
		t.Fatalf("Process error under keep-values: %v", err)
	}
	if bytes.Contains(out, []byte(key)) {
		t.Fatalf("AIza key not redacted: %s", out)
	}
	if !bytes.Contains(out, []byte(redactedToken)) {
		t.Fatalf("expected REDACTED marker: %s", out)
	}
	// keep-values preserves the real non-secret value and number.
	if !bytes.Contains(out, []byte("here")) || !bytes.Contains(out, []byte("42")) {
		t.Fatalf("keep-values should preserve non-secret values: %s", out)
	}
}

// Under --keep-values an unknown-format high-entropy token (no regex match)
// survives redaction, so the entropy quarantine in the gate must abort.
func TestKeepValuesEntropyQuarantineAborts(t *testing.T) {
	// 40 base64-ish random chars: high entropy, matches no known prefix.
	raw := []byte(`{"blob":"Zk9q3SxV1pLmTtRwYbNcHgJdFeUoIa72Qw0PzXyB"}`)

	s := &Sanitizer{keepValues: true}
	_, err := s.Process(raw, false)
	if err == nil {
		t.Fatal("entropy quarantine did not abort on a high-entropy token")
	}
	var leak *SecretLeakError
	if !errors.As(err, &leak) {
		t.Fatalf("expected *SecretLeakError, got %T", err)
	}
}

// Shape-scrub of an SSE stream keeps event:/comment/[DONE] framing byte-for-byte
// and scrubs only the data: JSON payloads.
func TestScrubSSEPreservesFraming(t *testing.T) {
	raw := []byte("event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"secret words"}` + "\n\n" +
		": a comment\n\n" +
		"data: [DONE]\n\n")

	out, err := scrubSSE(raw)
	if err != nil {
		t.Fatalf("scrubSSE: %v", err)
	}
	str := string(out)
	if !strings.Contains(str, "event: response.output_text.delta\n") {
		t.Errorf("event framing lost: %q", str)
	}
	if !strings.Contains(str, ": a comment\n") {
		t.Errorf("comment framing lost: %q", str)
	}
	if !strings.Contains(str, "data: [DONE]\n") {
		t.Errorf("[DONE] framing lost: %q", str)
	}
	if strings.Contains(str, "secret words") {
		t.Errorf("data payload free-text not scrubbed: %q", str)
	}
	// The enum-ish "type" value inside the data payload is preserved.
	if !strings.Contains(str, "response.output_text.delta") {
		t.Errorf("data payload enum value lost: %q", str)
	}
}

// Shape-scrub is deterministic: identical inputs (and inputs differing only in
// free-text) produce byte-identical output, which is what makes dedup collapse
// near-duplicate traffic.
func TestShapeScrubDeterministicAndCollapsing(t *testing.T) {
	a := []byte(`{"role":"user","text":"hello there"}`)
	b := []byte(`{"role":"user","text":"different content entirely"}`)

	sa, err := scrubJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := scrubJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sa, sb) {
		t.Fatalf("free-text-only difference did not collapse:\n a=%s\n b=%s", sa, sb)
	}

	again, _ := scrubJSON(a)
	if !bytes.Equal(sa, again) {
		t.Fatalf("scrub not deterministic:\n once=%s\n twice=%s", sa, again)
	}
}
