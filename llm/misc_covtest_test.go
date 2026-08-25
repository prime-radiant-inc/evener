package llm

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCovDeclaredKindResponseHeaderTimeout covers the declaredKind method
// on responseHeaderTimeoutError (errorkind.go line 132), which is otherwise
// never called via DeclaredKind.
func TestCovDeclaredKindResponseHeaderTimeout(t *testing.T) {
	err := newResponseHeaderTimeoutError("", "test timeout", nil)
	got := DeclaredKind(err)
	if got != KindTimeout {
		t.Fatalf("DeclaredKind(responseHeaderTimeoutError) = %q, want %q", got, KindTimeout)
	}
}

// TestCovDeclaredKindOnEachErrorType exercises declaredKind for every provider
// error type to ensure all the declaredKind methods (errorkind.go lines
// 127-138) are covered.
func TestCovDeclaredKindOnEachErrorType(t *testing.T) {
	cases := []struct {
		err  error
		want ErrorKind
	}{
		{ErrorFromHTTPStatus("p", 400, "bad request", nil, nil), KindInvalidRequest},
		{ErrorFromHTTPStatus("p", 401, "unauthorized", nil, nil), KindAuthentication},
		{ErrorFromHTTPStatus("p", 403, "forbidden", nil, nil), KindAccessDenied},
		{ErrorFromHTTPStatus("p", 404, "not found", nil, nil), KindNotFound},
		{ErrorFromHTTPStatus("p", 408, "timeout", nil, nil), KindTimeout},
		{ErrorFromHTTPStatus("p", 413, "too large", nil, nil), KindContextLength},
		{ErrorFromHTTPStatus("p", 400, "content filter blocked", nil, nil), KindContentFilter},
		{ErrorFromHTTPStatus("p", 429, "rate limited", nil, nil), KindRateLimit},
		{ErrorFromHTTPStatus("p", 500, "server error", nil, nil), KindServer},
		{ErrorFromHTTPStatus("p", 599, "unknown", nil, nil), KindUnknown},
	}
	for _, tc := range cases {
		got := DeclaredKind(tc.err)
		if got != tc.want {
			t.Fatalf("DeclaredKind(%T) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestCovDeclaredKindOnNonBearer covers DeclaredKind on an error that does not
// implement kindBearer (returns KindUnknown).
func TestCovDeclaredKindOnNonBearer(t *testing.T) {
	got := DeclaredKind(errors.New("generic error"))
	if got != KindUnknown {
		t.Fatalf("DeclaredKind(generic) = %q, want %q", got, KindUnknown)
	}
}

// TestCovDeclaredKindOnQuotaExceeded covers the quotaExceededError declaredKind
// (errorkind.go line 135).
func TestCovDeclaredKindOnQuotaExceeded(t *testing.T) {
	err := ErrorFromHTTPStatus("p", 400, "billing quota exceeded", nil, nil)
	got := DeclaredKind(err)
	if got != KindQuotaExceeded {
		t.Fatalf("DeclaredKind(quotaExceeded) = %q, want %q", got, KindQuotaExceeded)
	}
}

// TestCovTruncateForMessageInvalidUTF8 covers the invalid-UTF-8 loop in
// truncateForMessage (failuremessage.go lines 78-79). We craft a string
// that starts with valid UTF-8 then has an invalid byte boundary at the
// cut point.
func TestCovTruncateForMessageInvalidUTF8(t *testing.T) {
	// Build a string that is longer than maxFailureMessageBody and whose
	// truncation at maxFailureMessageBody produces an invalid UTF-8 prefix.
	// A multi-byte UTF-8 char that straddles the cut boundary.
	// 'é' is 2 bytes (0xC3 0xA9). We fill up to just before the cut with
	// ASCII, then place a multi-byte char that straddles the boundary.
	ascii := strings.Repeat("x", maxFailureMessageBody-1)
	// This puts 0xC3 at byte maxFailureMessageBody-1 and 0xA9 at
	// maxFailureMessageBody, so the cut at maxFailureMessageBody includes
	// 0xC3 but not 0xA9, producing invalid UTF-8.
	s := ascii + "é" + strings.Repeat("y", 100)
	got := truncateForMessage(s)
	want := ascii + "…"
	if got != want {
		t.Fatalf("truncateForMessage result length = %d and suffix %q, want exact valid prefix plus ellipsis", len(got), got[len(got)-10:])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateForMessage returned invalid UTF-8: %q", got[len(got)-10:])
	}
}

// TestCovRasterMediaTypeGIFFullDecodeError covers the gif.DecodeAll error
// path in RasterMediaType (media_utils.go lines 84-85).
func TestCovRasterMediaTypeGIFFullDecodeError(t *testing.T) {
	// Create a GIF that image.Decode accepts but gif.DecodeAll rejects.
	// A truncated multi-frame GIF can achieve this: the first frame decodes
	// but the second is incomplete.
	corrupted := createCorruptedMultiFrameGIF(t)
	if _, format, err := image.Decode(bytes.NewReader(corrupted)); err != nil || format != "gif" {
		t.Fatalf("fixture header decode = format %q, error %v; want gif success", format, err)
	}
	if _, err := gif.DecodeAll(bytes.NewReader(corrupted)); err == nil {
		t.Fatal("fixture unexpectedly passed full GIF decode")
	}
	got, err := RasterMediaType(corrupted)
	if err == nil || got != "" {
		t.Fatalf("RasterMediaType(corrupted GIF) = (%q, %v), want empty type and decode error", got, err)
	}
}

// TestCovRasterMediaTypeJPEG covers the jpeg format case in RasterMediaType
// (media_utils.go line 91).
func TestCovRasterMediaTypeJPEG(t *testing.T) {
	jpegData := createMinimalJPEG(t)
	got, err := RasterMediaType(jpegData)
	if err != nil {
		t.Fatalf("RasterMediaType(jpeg) error: %v", err)
	}
	if got != "image/jpeg" {
		t.Fatalf("RasterMediaType(jpeg) = %q, want image/jpeg", got)
	}
}

// TestCovAPITimeoutSourceForSSENonTimeout covers the non-sseReadTimeoutError
// return path in APITimeoutSourceForSSE (sse.go line 33-34).
func TestCovAPITimeoutSourceForSSENonTimeout(t *testing.T) {
	got := APITimeoutSourceForSSE(errors.New("some other error"))
	if got != APITimeoutNone {
		t.Fatalf("APITimeoutSourceForSSE(non-timeout) = %q, want %q", got, APITimeoutNone)
	}
}

// TestCovAPITimeoutSourceForSSETimeout covers the sseReadTimeoutError path.
func TestCovAPITimeoutSourceForSSETimeout(t *testing.T) {
	got := APITimeoutSourceForSSE(&sseReadTimeoutError{timeout: 5e9})
	if got != APITimeoutSSERead {
		t.Fatalf("APITimeoutSourceForSSE(sseReadTimeoutError) = %q, want %q", got, APITimeoutSSERead)
	}
}

// TestCovJSONInt64Float64 covers the float64 case in jsonInt64
// (usagelimit.go line 118).
func TestCovJSONInt64Float64(t *testing.T) {
	v, ok := jsonInt64(float64(42.0))
	if !ok || v != 42 {
		t.Fatalf("jsonInt64(float64(42)) = %d, %v, want 42, true", v, ok)
	}
}

// TestCovJSONInt64Int64 covers the int64 case in jsonInt64
// (usagelimit.go line 123).
func TestCovJSONInt64Int64(t *testing.T) {
	v, ok := jsonInt64(int64(99))
	if !ok || v != 99 {
		t.Fatalf("jsonInt64(int64(99)) = %d, %v, want 99, true", v, ok)
	}
}

// TestCovJSONInt64Int64Overflow covers the int64 overflow rejection.
func TestCovJSONInt64Int64Overflow(t *testing.T) {
	_, ok := jsonInt64(int64(1) << 62)
	if ok {
		t.Fatal("jsonInt64(overflow) should return false")
	}
}

// TestCovJSONInt64Float64Overflow covers the float64 overflow rejection.
func TestCovJSONInt64Float64Overflow(t *testing.T) {
	_, ok := jsonInt64(1e30)
	if ok {
		t.Fatal("jsonInt64(1e30) should return false")
	}
}

// TestCovJSONInt64Float64NegativeOverflow covers the float64 negative overflow.
func TestCovJSONInt64Float64NegativeOverflow(t *testing.T) {
	_, ok := jsonInt64(-1e30)
	if ok {
		t.Fatal("jsonInt64(-1e30) should return false")
	}
}

// TestCovJSONInt64Int64NegativeOverflow covers the int64 negative overflow.
func TestCovJSONInt64Int64NegativeOverflow(t *testing.T) {
	_, ok := jsonInt64(int64(-1) << 62)
	if ok {
		t.Fatal("jsonInt64(underflow) should return false")
	}
}

// TestCovJSONInt64Unknown covers the default return for an unrecognized type.
func TestCovJSONInt64Unknown(t *testing.T) {
	_, ok := jsonInt64("not-a-number")
	if ok {
		t.Fatal("jsonInt64(string) should return false")
	}
}

// createCorruptedMultiFrameGIF creates a GIF that image.Decode may accept
// for the first frame but gif.DecodeAll rejects.
func createCorruptedMultiFrameGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black})
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{img, img},
		Delay: []int{0, 0},
	}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// Truncate the data to make DecodeAll fail after the first frame.
	if len(data) > 30 {
		data = data[:len(data)-5]
	}
	return data
}

// createMinimalJPEG creates a valid minimal JPEG image.
func createMinimalJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
