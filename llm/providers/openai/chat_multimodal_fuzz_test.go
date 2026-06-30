package openai

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzOpenAIChatMultimodalParts drives buildChatMultimodalParts, the Chat
// Completions request builder that flattens text/image/document parts into the
// OpenAI content array. The fuzzer routes adversarial bytes into image/document
// payloads but never feeds a local-filesystem path (which would trigger a real
// os.ReadFile), so it validates wire-shape construction without touching disk.
//
// Oracles beyond no-panic:
//   - on success, every emitted entry re-marshals to valid JSON and carries a
//     legal "type" (text/image_url/file);
//   - count preservation: a text part always yields exactly one text entry, and
//     an image/document part with inline data always yields exactly one entry —
//     a silent drop reddens it;
//   - an Audio part is always rejected with an error (the documented unsupported
//     case), never silently emitted.
func FuzzOpenAIChatMultimodalParts(f *testing.F) {
	f.Add("hello", []byte{1, 2, 3}, "image/jpeg", "high", []byte("PDFDATA"), "doc.pdf", uint8(0))
	f.Add("", []byte{}, "", "", []byte{}, "", uint8(7))
	f.Add("text\x00with nul", []byte("\xff\xfe"), "image/png", "low", []byte{}, "", uint8(3))

	f.Fuzz(func(t *testing.T, text string, imgData []byte, imgMedia, imgDetail string, docData []byte, docName string, sel uint8) {
		parts := []llm.ContentPart{
			{Kind: llm.ContentText, Text: text},
			{Kind: llm.ContentImage, Image: &llm.ImageData{
				Data: imgData, MediaType: imgMedia, Detail: imgDetail,
			}},
			{Kind: llm.ContentDocument, Document: &llm.DocumentData{
				Data: docData, MediaType: "application/pdf", FileName: docName,
			}},
		}

		out, err := buildChatMultimodalParts(parts)
		if err != nil {
			t.Fatalf("buildChatMultimodalParts errored on text+inline-image+inline-doc: %v", err)
		}

		// Exactly: one text entry, one image entry (data present makes it non-empty),
		// one file entry (data present). Nothing dropped, nothing invented.
		gotText, gotImage, gotFile := 0, 0, 0
		for i, entry := range out {
			if _, merr := json.Marshal(entry); merr != nil {
				t.Fatalf("entry[%d] unmarshalable: %v\nentry=%#v", i, merr, entry)
			}
			switch entry["type"] {
			case "text":
				gotText++
			case "image_url":
				gotImage++
				if _, ok := entry["image_url"].(map[string]any); !ok {
					t.Fatalf("image_url entry missing nested object: %#v", entry)
				}
			case "file":
				gotFile++
			default:
				t.Fatalf("entry[%d] illegal type %v", i, entry["type"])
			}
		}
		if gotText != 1 {
			t.Fatalf("text parts emitted=%d, want 1", gotText)
		}
		// An inline image with non-empty data always produces an entry.
		if len(imgData) > 0 && gotImage != 1 {
			t.Fatalf("inline image emitted=%d, want 1 (data len=%d)", gotImage, len(imgData))
		}
		if len(docData) > 0 && gotFile != 1 {
			t.Fatalf("inline document emitted=%d, want 1 (data len=%d)", gotFile, len(docData))
		}

		// An Audio part must always be rejected, never silently dropped.
		if sel&1 == 1 {
			audioParts := []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: []byte{1}}}}
			if _, aerr := buildChatMultimodalParts(audioParts); aerr == nil {
				t.Fatalf("buildChatMultimodalParts accepted an audio part, want unsupported error")
			}
		}
	})
}
