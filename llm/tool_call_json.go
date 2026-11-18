package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"strconv"
	"unicode/utf8"
)

type toolCallFields ToolCallData

// MarshalJSON keeps arguments available as structured JSON. A spelling
// companion recovers the exact provider input when embedding changes its bytes.
func (tc ToolCallData) MarshalJSON() ([]byte, error) {
	record := struct {
		toolCallFields
		ArgumentBytes    []byte          `json:"arguments_bytes,omitempty"`
		ArgumentSpelling json.RawMessage `json:"arguments_spelling,omitempty"`
	}{toolCallFields: toolCallFields(tc)}
	if len(tc.Arguments) != 0 && tc.RawArguments == "" {
		stored, err := json.Marshal(tc.Arguments)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(stored, tc.Arguments) {
			record.ArgumentSpelling = compactArgumentSpelling(stored, tc.Arguments)
			if len(record.ArgumentSpelling) == 0 {
				record.ArgumentBytes = tc.Arguments
			}
		}
	}
	return json.Marshal(record)
}

// spellingBytes decodes damaged optional bytes as nil. The enclosing spelling
// and edits still use the caller's policy for unknown fields.
type spellingBytes []byte

func (b *spellingBytes) UnmarshalJSON(data []byte) error {
	var decoded []byte
	if err := json.Unmarshal(data, &decoded); err != nil {
		decoded = nil
	}
	*b = decoded
	return nil
}

type spellingOffset int

func (offset *spellingOffset) UnmarshalJSON(data []byte) error {
	value, err := strconv.Atoi(string(data))
	if err != nil {
		value = -1 // Recovery rejects offsets outside the structured arguments.
	}
	*offset = spellingOffset(value)
	return nil
}

// argumentEdit restores a run of original whitespace or string escaping at a
// byte offset in the structured JSON. Unchanged argument values are stored once.
type argumentEdit struct {
	At    spellingOffset `json:"at"`
	Drop  spellingOffset `json:"drop,omitempty"`
	Bytes spellingBytes  `json:"bytes"`
}

type argumentEdits []argumentEdit

type argumentSpelling struct {
	Digest spellingBytes `json:"sha256"`
	Edits  argumentEdits `json:"edits"`
}

// Decode expected containers with the transcript reader's unknown-member
// policy. A malformed known value invalidates the spelling without preventing
// the reader from validating the remaining fields of the transcript.
func decodeSpellingPart[T any](dec *jsontext.Decoder, kind jsontext.Kind, part *T) error {
	if dec.PeekKind() == kind {
		return jsonv2.UnmarshalDecode(dec, part)
	}
	var invalid T
	*part = invalid
	return dec.SkipValue()
}

func (edit *argumentEdit) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	type fields argumentEdit
	edit.At = -1 // Every stored edit names its starting offset.
	return decodeSpellingPart(dec, '{', (*fields)(edit))
}

func (edits *argumentEdits) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return decodeSpellingPart(dec, '[', (*[]argumentEdit)(edits))
}

func (spelling *argumentSpelling) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	type fields argumentSpelling
	return decodeSpellingPart(dec, '{', (*fields)(spelling))
}

func compactArgumentSpelling(stored, original []byte) json.RawMessage {
	fullSize := base64.StdEncoding.EncodedLen(len(original))
	minSize := base64.StdEncoding.EncodedLen(sha256.Size)
	if fullSize <= minSize {
		return nil
	}
	var edits []argumentEdit
	i, j := 0, 0
	for j < len(original) {
		if i < len(stored) && stored[i] == original[j] {
			i++
			j++
			continue
		}
		start, from := i, j
		for j < len(original) && (i == len(stored) || stored[i] != original[j]) {
			switch original[j] {
			case ' ', '\t', '\r', '\n':
				j++
			default:
				// Embedding raw JSON can spell a literal character as a Unicode escape.
				if len(stored)-i < 6 || stored[i] != '\\' || stored[i+1] != 'u' {
					return nil
				}
				code, err := strconv.ParseInt(string(stored[i+2:i+6]), 16, 32)
				r, size := utf8.DecodeRune(original[j:])
				if err != nil || code != int64(r) {
					return nil
				}
				i += 6
				j += size
			}
		}
		// Stop collecting dense edits once their smallest encoding already
		// costs as much as the complete original.
		minSize += len(`{"at":0,"bytes":""}`) + base64.StdEncoding.EncodedLen(j-from)
		if minSize >= fullSize {
			return nil
		}
		edits = append(edits, argumentEdit{At: spellingOffset(start), Drop: spellingOffset(i - start), Bytes: original[from:j]})
	}
	if i != len(stored) {
		return nil
	}
	digest := sha256.Sum256(stored)
	encoded, err := json.Marshal(argumentSpelling{Digest: digest[:], Edits: edits})
	if err != nil || len(encoded)+len("arguments_spelling") >= fullSize+2+len("arguments_bytes") {
		return nil
	}
	return encoded
}

func (format *argumentSpelling) restore(stored []byte) []byte {
	digest := sha256.Sum256(stored)
	if len(format.Edits) == 0 || !bytes.Equal(digest[:], format.Digest) {
		return nil
	}
	original := make([]byte, 0, len(stored))
	pos := 0
	for _, edit := range format.Edits {
		at, drop := int(edit.At), int(edit.Drop)
		if edit.Bytes == nil || at < pos || at > len(stored) || drop < 0 || drop > len(stored)-at {
			return nil
		}
		original = append(original, stored[pos:at]...)
		original = append(original, edit.Bytes...)
		pos = at + drop
	}
	return append(original, stored[pos:]...)
}

// UnmarshalJSONFrom restores the original spelling only for the same structured
// argument value. A missing or inconsistent companion leaves arguments intact.
func (tc *ToolCallData) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	record := struct {
		*toolCallFields
		ArgumentBytes    json.RawMessage   `json:"arguments_bytes"`
		ArgumentSpelling *argumentSpelling `json:"arguments_spelling"`
	}{toolCallFields: (*toolCallFields)(tc)}
	if err := jsonv2.UnmarshalDecode(dec, &record); err != nil {
		return err
	}
	if (len(record.ArgumentBytes) == 0 && record.ArgumentSpelling == nil) || len(tc.Arguments) == 0 || tc.RawArguments != "" {
		return nil
	}
	stored, err := json.Marshal(tc.Arguments)
	if err != nil {
		return err
	}
	var original []byte
	if record.ArgumentSpelling != nil {
		original = record.ArgumentSpelling.restore(stored)
	} else if err := json.Unmarshal(record.ArgumentBytes, &original); err != nil {
		// A damaged optional spelling leaves the structured arguments usable.
		original = nil
	}
	if len(original) != 0 {
		encoded, err := json.Marshal(json.RawMessage(original))
		if err == nil && bytes.Equal(encoded, stored) {
			tc.Arguments = original
		}
	}
	return nil
}
