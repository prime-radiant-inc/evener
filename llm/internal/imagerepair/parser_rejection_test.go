package imagerepair

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"testing"
)

// Bytes helpers for the ICC curve encodings: the tag signature, the reserved
// zero, and s15.16 fixed-point parameters.
func curvTag(count uint32, entries []byte) []byte {
	b := make([]byte, 12+len(entries))
	copy(b, "curv")
	binary.BigEndian.PutUint32(b[8:], count)
	copy(b[12:], entries)
	return b
}

func paraTag(kind uint16, params ...float64) []byte {
	b := make([]byte, 12+4*len(params))
	copy(b, "para")
	binary.BigEndian.PutUint16(b[8:], kind)
	for i, p := range params {
		binary.BigEndian.PutUint32(b[12+4*i:], uint32(int32(p*65536)))
	}
	return b
}

// TestParseCurveRejections pins every arithmetic-domain rejection of the
// curve parser, each with a valid encoding of the same shape as the control
// so the rejection is attributable to the violated rule alone.
func TestParseCurveRejections(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		tag  []byte
	}{
		{"curv count disagrees with the tag length", curvTag(5, []byte{0, 1})},
		{"curv gamma of zero", curvTag(1, []byte{0, 0})},
		{"curv gamma above sixteen", curvTag(1, []byte{0x10, 0x01})},
		{"para length disagrees with the function kind", paraTag(0, 2.2)[:15]},
		{"para kind one with a breakpoint above one", paraTag(1, 1, 1, -2)},
		{"para kind three with a negative segment c", paraTag(3, 1, 1, 0, -1, 0.5)},
		{"para kind two whose upper branch leaves the range", paraTag(2, 1, 1, -0.5, 2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseCurve(ctx, tc.tag); ok {
				t.Fatal("curve accepted an encoding that violates its arithmetic domain")
			}
		})
	}
	// Controls: the same encodings with parameters inside the domain parse.
	for name, tag := range map[string][]byte{
		"identity":   curvTag(0, nil),
		"gamma":      curvTag(1, []byte{0x01, 0x00}),
		"sampled":    curvTag(2, []byte{0, 0, 0, 100}),
		"para zero":  paraTag(0, 2.2),
		"para one":   paraTag(1, 1, 1, 0),
		"para two":   paraTag(2, 1, 1, 0, 0),
		"para three": paraTag(3, 1, 1, 0, 1, 0.5),
		"para four":  paraTag(4, 1, 1, 0, 1, 0, 0, 0),
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			if _, ok := parseCurve(ctx, tag); !ok {
				t.Fatal("curve rejected a valid encoding")
			}
		})
	}
	// Cancellation is honored inside the sampled-table walk on its first
	// cooperative check.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := parseCurve(cancelled, curvTag(2, []byte{0, 0, 0, 100})); ok {
		t.Fatal("a cancelled context must not produce a curve")
	}
}

// TestParseICCRejections pins the profile-level rejection arms a mutation of
// a single field can reach, with the unmutated profile as the control.
func TestParseICCRejections(t *testing.T) {
	ctx := context.Background()
	base := fixture(t, "p3.icc")
	if _, ok := parseICC(ctx, base); !ok {
		t.Fatal("reference profile must parse")
	}
	mutations := map[string]func([]byte){
		"rendering intent above three": func(p []byte) { binary.BigEndian.PutUint32(p[64:], 4) },
		"missing white point tag": func(p []byte) {
			entry, _, _ := profileTag(t, p, "wtpt")
			copy(p[entry:], "wtpx")
		},
		"white point tag of the wrong type": func(p []byte) {
			_, off, _ := profileTag(t, p, "wtpt")
			copy(p[off:], "XYZx")
		},
		"chad tag of the wrong type": func(p []byte) {
			_, off, _ := profileTag(t, p, "chad")
			copy(p[off:], "sf33")
		},
		"missing red colorant tag": func(p []byte) {
			entry, _, _ := profileTag(t, p, "rXYZ")
			copy(p[entry:], "rXYz")
		},
		"matrix white disagrees with the PCS illuminant": func(p []byte) {
			_, off, _ := profileTag(t, p, "rXYZ")
			binary.BigEndian.PutUint32(p[off+8:], 0x00012000) // X +0.125
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := bytes.Clone(base)
			mutate(p)
			if _, ok := parseICC(ctx, p); ok {
				t.Fatal("profile accepted a field that violates the v4 input subset")
			}
		})
	}
}

// minimalJPEG assembles a structurally complete single-frame JPEG: SOI, SOF0,
// SOS, the given interior bytes (entropy or marker fill), and EOI.
func minimalJPEG(interior []byte) []byte {
	out := []byte{0xFF, 0xD8}
	out = append(out, segment(0xC0, make([]byte, 6))...)
	out = append(out, segment(0xDA, nil)...)
	out = append(out, interior...)
	return append(out, 0xFF, 0xD9)
}

// TestScanJPEGRejections pins the structural refusals of the scanner, each
// against the control of a complete minimal JPEG so the refusal is the
// specific violation's alone.
func TestScanJPEGRejections(t *testing.T) {
	ctx := context.Background()
	if _, ok := scanJPEG(ctx, minimalJPEG(nil)); !ok {
		t.Fatal("a complete minimal JPEG must scan")
	}
	sixByteSOF := segment(0xC0, make([]byte, 6))
	cases := map[string][]byte{
		"standalone TEM marker":                {0xFF, 0xD8, 0xFF, 0x01},
		"marker header cut before the length":  {0xFF, 0xD8, 0xFF, 0xE0},
		"scan before any frame":                append([]byte{0xFF, 0xD8}, segment(0xDA, nil)...),
		"second frame":                         append(append([]byte{0xFF, 0xD8}, sixByteSOF...), sixByteSOF...),
		"stream ends without EOI":              append([]byte{0xFF, 0xD8}, segment(0xFE, []byte("x"))...),
		"entropy runs to EOF without a marker": minimalJPEG(nil)[:len(minimalJPEG(nil))-2],
		"marker count over budget":             append(append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0xFF, 0xFE, 0x00, 0x02}, 65_534)...), minimalJPEG(nil)[2:]...),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := scanJPEG(ctx, b); ok {
				t.Fatal("scanner accepted a structurally unsafe JPEG")
			}
		})
	}
	// The boundary control: one fill marker fewer stays inside the budget and
	// completes, so the refusal above is the budget's and not the layout's.
	within := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0xFF, 0xFE, 0x00, 0x02}, 65_533)...)
	if _, ok := scanJPEG(ctx, append(within, minimalJPEG(nil)[2:]...)); !ok {
		t.Fatal("a JPEG at exactly the marker budget must scan")
	}
}

// TestScanJPEGCancellationBoundaries sweeps cancellation across every
// cooperative check the scanner reaches for two crafted inputs: a long
// entropy run with no 0xFF byte (the in-scan step check) and a long 0xFF
// marker fill (the fill step check). Real photos never produce runs this
// long, so the crafted inputs are what make those boundaries reachable at
// all; the sweep must eventually complete, proving every earlier boundary
// returned the refused scan.
func TestScanJPEGCancellationBoundaries(t *testing.T) {
	inputs := map[string][]byte{
		"entropy step check": minimalJPEG(bytes.Repeat([]byte{0}, 8192)),
		"fill step check":    minimalJPEG(bytes.Repeat([]byte{0xFF}, 8190)),
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			completed := false
			for n := 1; n <= 64; n++ {
				ctx, cancel := context.WithCancel(context.Background())
				c := &cancelAfterChecks{Context: ctx, cancel: cancel, remaining: n}
				_, ok := scanJPEG(c, input)
				cancel()
				if ok {
					completed = true
					break
				}
			}
			if !completed {
				t.Fatal("scan never completed: the sweep must reach a run with no cancellation")
			}
		})
	}
}

// TestRepairJPEGRemovalLoopCancellation sweeps cancellation across the
// ICC-strip pass of a fallback repair (a malformed profile declines
// conversion, so the removal loop is the only path); every cancelled run
// must hand back the original bytes, and some run must complete.
func TestRepairJPEGRemovalLoopCancellation(t *testing.T) {
	input := tagged(fixture(t, "patches.jpg"), []byte("malformed"))
	original := bytes.Clone(input)
	completed := false
	for n := 1; n <= 400; n++ {
		ctx, cancel := context.WithCancel(context.Background())
		c := &cancelAfterChecks{Context: ctx, cancel: cancel, remaining: n}
		r, err := RepairJPEG(c, input, ProfileFree)
		cancel()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("run %d err = %v, want the cancellation", n, err)
			}
			if r.Method != Unchanged || !bytes.Equal(r.Data, input) {
				t.Fatalf("run %d returned a partial result: %+v", n, r.Method)
			}
			continue
		}
		if !bytes.Equal(input, original) {
			t.Fatal("source mutated")
		}
		if r.Method != ProfileRemoved {
			t.Fatalf("completed run method = %v, want ProfileRemoved", r.Method)
		}
		completed = true
		break
	}
	if !completed {
		t.Fatal("the removal loop never completed")
	}
}

// A grayscale JPEG tagged with a color profile must not be color-managed: the
// transform has no business re-interpreting gray pixels, so the repair falls
// back to stripping the profile and returns the original bytes.
func TestRepairGrayJPEGWithColorProfileFallsBack(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 32, 24)), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	j := buf.Bytes()
	r := call(t, tagged(j, fixture(t, "p3.icc")), PreferColorManaged, ProfileRemoved)
	if !bytes.Equal(r.Data, j) {
		t.Fatal("fallback changed the gray JPEG bytes")
	}
}

// Only the orientation tag decides rotation; an IFD0 holding any other tag
// still parses and defaults to the identity, so unrelated metadata can never
// disable a repair.
func TestParseOrientationSkipsUnrelatedTags(t *testing.T) {
	tiff := exif(8, binary.LittleEndian)[10:]
	binary.LittleEndian.PutUint16(tiff[10:], 0x010f) // the Make tag, not orientation
	o, ok := parseOrientation(tiff)
	if !ok || o != 1 {
		t.Fatalf("parseOrientation = (%d, %v), want the identity default (1, true)", o, ok)
	}
}
