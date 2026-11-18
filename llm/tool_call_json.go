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

// argumentEdit restores a run of original whitespace or string escaping at a
// byte offset in the structured JSON. Unchanged argument values are stored once.
type argumentEdit struct {
	At    int           `json:"at"`
	Drop  int           `json:"drop,omitempty"`
	Bytes spellingBytes `json:"bytes"`
}

type argumentSpelling struct {
	Digest spellingBytes  `json:"sha256"`
	Edits  []argumentEdit `json:"edits"`
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
		edits = append(edits, argumentEdit{At: start, Drop: i - start, Bytes: original[from:j]})
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
		if edit.Bytes == nil || edit.At < pos || edit.At > len(stored) || edit.Drop < 0 || edit.Drop > len(stored)-edit.At {
			return nil
		}
		original = append(original, stored[pos:edit.At]...)
		original = append(original, edit.Bytes...)
		pos = edit.At + edit.Drop
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
