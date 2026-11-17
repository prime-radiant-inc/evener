package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

// TestDecodeEntryOnePassMatchesDecodeEntry: for every line the resume scan can
// meet, decodeEntryOnePass must return exactly what DecodeEntry returns — the
// same entry, or the same error value. The fast parse is an optimization of the
// success path, never a second opinion on validity, so this is the property the
// scan's correctness rests on.
func TestDecodeEntryOnePassMatchesDecodeEntry(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"valid entry":            `{"kind":"entry","seq":1,"turn":{"format":1,"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"header-shaped":          `{"kind":"header","format_version":2,"session_id":"s"}`,
		"non-entry kind":         `{"kind":"summary","seq":1}`,
		"malformed":              `{not json}`,
		"empty":                  ``,
		"unknown top field":      `{"kind":"entry","seq":1,"nonesuch":true,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"unknown nested field":   `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","nonesuch":1,"message":{"role":"user","content":[]}}}`,
		"unknown in part":        `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"x","nonesuch":1}]}}}`,
		"unknown in usage":       `{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[]},"usage":{"input_tokens":1,"nonesuch":1}}}`,
		"multiple values":        `{"kind":"entry","turn":{}}{"kind":"entry","turn":{}}`,
		"trailing garbage":       `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}},`,
		"duplicate members":      `{"kind":"entry","seq":7,"seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"lone surrogate":         "{\"kind\":\"entry\",\"seq\":1,\"turn\":{\"kind\":\"USER_INPUT\",\"message\":{\"role\":\"user\",\"content\":[{\"kind\":\"text\",\"text\":\"\\ud800\"}]}}}",
		"invalid utf8":           "{\"kind\":\"entry\",\"seq\":1,\"turn\":{\"kind\":\"USER_INPUT\",\"message\":{\"role\":\"user\",\"content\":[{\"kind\":\"text\",\"text\":\"\xff\"}]}}}",
		"dash-folded name":       `{"k-ind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"underscore-folded name": `{"k_ind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"upper-case name":        `{"Kind":"entry","SEQ":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"null line":              `null`,
		"empty object":           `{}`,
		"seq as string":          `{"kind":"entry","seq":"1","turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"seq float":              `{"kind":"entry","seq":1.0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"seq out of range":       `{"kind":"entry","seq":9223372036854775808,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		"bad timestamp":          `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","timestamp":"not-a-time","message":{"role":"user","content":[]}}}`,
		"bad base64":             `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"image","image":{"data":"!!!!"}}]}}}`,
		"bad raw arguments":      `{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c","name":"n","arguments":{not json}}}]}}}`,
		"notice without payload": `{"kind":"entry","seq":1,"turn":{"kind":"NOTICE","message":{"role":"user","content":[]}}}`,
		"notice invalid payload": `{"kind":"entry","seq":1,"turn":{"kind":"NOTICE","message":{"role":"user","content":[]},"notice":{"kind":"TOOL_REPAIR"}}}`,
		"pre-flag block notice":  "{\"kind\":\"entry\",\"seq\":1,\"turn\":{\"kind\":\"STEERING\",\"message\":{\"role\":\"user\",\"content\":[{\"kind\":\"text\",\"text\":\"<evener-notification>tool repaired</evener-notification>\"}]}}}",
		"machinery flagged":      `{"kind":"entry","machinery_flagged":true,"seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"<evener-notification>x</evener-notification>"}]}}}`,
		"no kind":                `{"seq":1,"turn":{}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want, wantErr := DecodeEntry([]byte(line))
			got, gotErr := decodeEntryOnePass([]byte(line))
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("verdict differs: DecodeEntry=%v, one-pass=%v", wantErr, gotErr)
			}
			if wantErr != nil {
				// The error must be DecodeEntry's own value, sentinels included.
				if !errors.Is(gotErr, wantErr) && gotErr.Error() != wantErr.Error() {
					t.Fatalf("error differs: DecodeEntry=%v, one-pass=%v", wantErr, gotErr)
				}
				var wantLine *LineError
				var gotLine *LineError
				if errors.As(wantErr, &wantLine) != errors.As(gotErr, &gotLine) {
					t.Fatalf("error type differs: DecodeEntry=%T, one-pass=%T", wantErr, gotErr)
				}
				for _, sentinel := range []error{ErrUnsupportedFormat, ErrInvalidRecordBoundary, ErrLineTooLong} {
					if errors.Is(wantErr, sentinel) != errors.Is(gotErr, sentinel) {
						t.Fatalf("sentinel %v: DecodeEntry=%v one-pass=%v", sentinel, errors.Is(wantErr, sentinel), errors.Is(gotErr, sentinel))
					}
				}
				return
			}
			if !reflect.DeepEqual(want, got) {
				ja, _ := json.Marshal(want)
				jb, _ := json.Marshal(got)
				t.Fatalf("entry differs:\n DecodeEntry=%s\n one-pass   =%s", ja, jb)
			}
		})
	}
}

// TestDecodeEntryOnePassMatchesDecodeEntryOnRealTranscript replays a real
// transcript's lines through both decoders. It is the same property over data
// nothing in this repo authored, and it is skipped when the file is absent so
// the suite stays deterministic.
func TestDecodeEntryOnePassMatchesDecodeEntryOnRealTranscript(t *testing.T) {
	t.Parallel()
	path := os.Getenv("EVENER_RESUME_DECODE_CORPUS")
	if path == "" {
		t.Skip("set EVENER_RESUME_DECODE_CORPUS to a transcript to replay it")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	start := 0
	seen := 0
	for i := 0; i <= len(data); i++ {
		if i != len(data) && data[i] != '\n' {
			continue
		}
		line := data[start:i]
		start = i + 1
		if len(line) == 0 {
			continue
		}
		want, wantErr := DecodeEntry(line)
		got, gotErr := decodeEntryOnePass(line)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("line %d verdict differs: DecodeEntry=%v, one-pass=%v", seen+1, wantErr, gotErr)
		}
		if wantErr == nil && !reflect.DeepEqual(want, got) {
			t.Fatalf("line %d entry differs", seen+1)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("corpus held no lines")
	}
	t.Logf("replayed %d lines identically", seen)
}
