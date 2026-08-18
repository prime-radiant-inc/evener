//go:build serffuzz

package apilog

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// FuzzAPILogStreamRecovery drives the apilog DECODE side with arbitrary bytes:
// a corrupt, truncated, or interleaved api.jsonl, which is exactly the file
// state a crashed or killed evener leaves behind. FuzzCanonicalRecordCodec next
// door covers the encode path with well-formed records; nothing covered the
// recovery path that has to make sense of the wreckage, and it was the least
// fuzz-covered code in the module.
//
// Oracles, none of which is "no panic":
//
//   - Termination and progress. Decoding must end, and every record it yields
//     must come from a strictly increasing offset. A decoder that returns the
//     same offset twice is looping, which on a large log is a hang rather than
//     an error.
//   - Reported offsets stay inside the file. ScanRecovery's whole job is to hand
//     a caller an offset it will TRUNCATE to; an offset past the end, or a
//     negative one, destroys data rather than recovering it.
//   - Recovery means what it says. Everything before lastCompleteOffset must
//     decode without error, because that is the definition of "the last complete
//     record ends here". This is the oracle that would catch a boundary scan
//     that lands mid-record.
//   - partialTail agrees with the bytes. A tail is partial exactly when the
//     recovered prefix is shorter than the file.
//
// Pure byte decoding over an in-memory reader: nothing here touches the
// filesystem or executes a handler.
func FuzzAPILogStreamRecovery(f *testing.F) {
	f.Add([]byte(""), 1024)
	f.Add([]byte("\n"), 1024)
	f.Add([]byte("{}\n"), 1024)
	f.Add([]byte("{\"v\":1}\n{\"v\":2}\n"), 1024)
	f.Add([]byte("{\"v\":1}\n{\"trunc"), 1024)
	f.Add([]byte("not json at all\n"), 1024)
	f.Add([]byte("{\"v\":1}\n"), 1)
	f.Add(bytes.Repeat([]byte("x"), 300), 64)
	f.Add([]byte{0, '\n', 0xff, '\n'}, 1024)

	f.Fuzz(func(t *testing.T, data []byte, maxLineBytes int) {
		// Keep the input in a range a real log occupies; the interesting shapes
		// are boundaries, not size.
		if len(data) > 1<<16 {
			data = data[:1<<16]
		}
		if maxLineBytes < 1 || maxLineBytes > 1<<16 {
			maxLineBytes = 1024
		}

		decoder := NewDecoder(bytes.NewReader(data), maxLineBytes)
		lastOffset := int64(-1)
		for range 4096 {
			_, err := decoder.Next()
			offset := decoder.RecordOffset()
			if offset < 0 || offset > int64(len(data)) {
				t.Fatalf("RecordOffset() = %d, outside the %d-byte input", offset, len(data))
			}
			if err != nil {
				break
			}
			if offset <= lastOffset {
				t.Fatalf("decoder did not advance: offset %d after %d", offset, lastOffset)
			}
			lastOffset = offset
		}

		offset, partialTail, err := ScanRecovery(bytes.NewReader(data), maxLineBytes)
		if err != nil {
			// A refusal must not also claim a usable boundary, or a caller that
			// logs the error and truncates anyway loses good records.
			if partialTail {
				t.Fatalf("ScanRecovery reported an error and partialTail together: %v", err)
			}
			return
		}
		if offset < 0 || offset > int64(len(data)) {
			t.Fatalf("ScanRecovery offset %d is outside the %d-byte input", offset, len(data))
		}
		if partialTail != (offset < int64(len(data))) {
			t.Fatalf("partialTail=%v but offset %d of %d bytes", partialTail, offset, len(data))
		}

		// The recovered prefix must decode cleanly all the way through: that is
		// precisely the claim "the last complete record ends at this offset".
		prefix := NewDecoder(bytes.NewReader(data[:offset]), maxLineBytes)
		for range 4096 {
			if _, err := prefix.Next(); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("recovered prefix of %d bytes failed to decode at offset %d: %v",
					offset, prefix.RecordOffset(), err)
			}
		}
	})
}
