package imagerepair

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func segment(marker byte, body []byte) []byte {
	b := []byte{255, marker, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(body)+2))
	return append(b, body...)
}
func inject(j []byte, parts ...[]byte) []byte {
	out := append([]byte{}, j[:2]...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return append(out, j[2:]...)
}
func iccChunk(profile []byte, seq, total byte) []byte {
	b := append([]byte("ICC_PROFILE\x00"), seq, total)
	return segment(0xe2, append(b, profile...))
}
func tagged(j, p []byte) []byte { return inject(j, iccChunk(p, 1, 1)) }
func exif(o uint16, order binary.ByteOrder) []byte {
	b := make([]byte, 32)
	copy(b, "Exif\x00\x00")
	if order == binary.LittleEndian {
		copy(b[6:], "II")
	} else {
		copy(b[6:], "MM")
	}
	order.PutUint16(b[8:], 42)
	order.PutUint32(b[10:], 8)
	order.PutUint16(b[14:], 1)
	order.PutUint16(b[16:], 0x112)
	order.PutUint16(b[18:], 3)
	order.PutUint32(b[20:], 1)
	order.PutUint16(b[24:], o)
	return segment(0xe1, b)
}
func call(t testing.TB, data []byte, mode Mode, want Method) Result {
	t.Helper()
	original := bytes.Clone(data)
	r, e := RepairJPEG(context.Background(), data, mode)
	if e != nil {
		t.Fatal(e)
	}
	if r.Method != want {
		t.Fatalf("method = %v, want %v", r.Method, want)
	}
	if !bytes.Equal(original, data) {
		t.Fatal("source mutated")
	}
	if want == Unchanged && !bytes.Equal(r.Data, data) {
		t.Fatal("unchanged bytes differ")
	}
	return r
}
func decodedPNG(t testing.TB, r Result) image.Image {
	t.Helper()
	if r.MediaType != "image/png" {
		t.Fatalf("media type %v", r.MediaType)
	}
	im, e := png.Decode(bytes.NewReader(r.Data))
	if e != nil {
		t.Fatal(e)
	}
	return im
}
func rgba(c color.Color) color.RGBA { return color.RGBAModel.Convert(c).(color.RGBA) }
func comparePixels(t *testing.T, a, b image.Image, tolerance int) {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("bounds %v != %v", a.Bounds(), b.Bounds())
	}
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			p, q := rgba(a.At(x, y)), rgba(b.At(x, y))
			for i, d := range []int{int(p.R) - int(q.R), int(p.G) - int(q.G), int(p.B) - int(q.B)} {
				if d < -tolerance || d > tolerance {
					t.Fatalf("pixel (%d,%d) channel %d: %v != %v", x, y, i, p, q)
				}
			}
			if p.A != 255 {
				t.Fatal("nonopaque PNG")
			}
		}
	}
}

// Catches missing repair, pixel changes in untagged sRGB, metadata leakage and alias mutation.
func TestRepairCleanJPEG(t *testing.T) {
	j := fixture(t, "patches.jpg")
	original, e := jpeg.Decode(bytes.NewReader(j))
	if e != nil {
		t.Fatal(e)
	}
	r := call(t, inject(j, segment(0xfe, []byte("synthetic comment"))), PreferColorManaged, ColorManaged)
	comparePixels(t, decodedPNG(t, r), original, 0)
	// A clean PNG contains only required image chunks, not EXIF or ICC metadata.
	chunks := []string{}
	for off := 8; off < len(r.Data); {
		n := int(binary.BigEndian.Uint32(r.Data[off:]))
		chunks = append(chunks, string(r.Data[off+4:off+8]))
		off += 12 + n
	}
	for _, c := range chunks {
		if c != "IHDR" && c != "IDAT" && c != "IEND" {
			t.Fatalf("unexpected PNG chunk %v", c)
		}
	}
	call(t, j, ProfileFree, Unchanged)
}

// Catches selecting profiles by name, omitted TRCs, missing D50 adaptation and wrong clipping.
func TestRepairLittleCMSReference(t *testing.T) {
	var refs struct {
		Profiles map[string][]struct{ Input, Output [3]uint8 }
	}
	if e := json.Unmarshal(fixture(t, "samples.json"), &refs); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"p3", "srgb"} {
		t.Run(name, func(t *testing.T) {
			if len(refs.Profiles[name]) == 0 {
				t.Fatal("missing independent color samples")
			}
			profile := fixture(t, name+".icc")
			transform, ok := parseICC(context.Background(), profile)
			if !ok {
				t.Fatal("reference profile not supported")
			}
			compareColorSamples(t, transform, refs.Profiles[name])
			r := call(t, tagged(fixture(t, "patches.jpg"), profile), PreferColorManaged, ColorManaged)
			reference, e := png.Decode(bytes.NewReader(fixture(t, name+"-reference.png")))
			if e != nil {
				t.Fatal(e)
			}
			comparePixels(t, decodedPNG(t, r), reference, 3) // Independent libjpeg vs Go IDCT rounding.
		})
	}
}

func compareColorSamples(t *testing.T, transform *iccTransform, samples []struct{ Input, Output [3]uint8 }) {
	t.Helper()
	for _, sample := range samples {
		out := transform.rgb(sample.Input[0], sample.Input[1], sample.Input[2])
		for i, v := range []uint8{out.R, out.G, out.B} {
			d := int(v) - int(sample.Output[i])
			if d < -1 || d > 1 {
				t.Fatalf("sample %v: %v != %v", sample.Input, out, sample.Output)
			}
		}
	}
}

// Literal row layouts, not the production coordinate mapping, are the orientation oracle.
func TestRepairOrientations(t *testing.T) {
	im := image.NewGray(image.Rect(0, 0, 3, 2))
	copy(im.Pix, []byte{20, 60, 100, 140, 180, 220})
	var b bytes.Buffer
	if e := jpeg.Encode(&b, im, &jpeg.Options{Quality: 100}); e != nil {
		t.Fatal(e)
	}
	original, e := jpeg.Decode(bytes.NewReader(b.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	layouts := [][]int{{0, 1, 2, 3, 4, 5}, {2, 1, 0, 5, 4, 3}, {5, 4, 3, 2, 1, 0}, {3, 4, 5, 0, 1, 2}, {0, 3, 1, 4, 2, 5}, {3, 0, 4, 1, 5, 2}, {5, 2, 4, 1, 3, 0}, {2, 5, 1, 4, 0, 3}}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for o, layout := range layouts {
			r := call(t, inject(b.Bytes(), exif(uint16(o+1), order)), PreferColorManaged, ColorManaged)
			got := decodedPNG(t, r)
			w, h := 3, 2
			if o >= 4 {
				w, h = 2, 3
			}
			if got.Bounds() != image.Rect(0, 0, w, h) {
				t.Fatalf("orientation %d bounds %v", o+1, got.Bounds())
			}
			for i, src := range layout {
				if rgba(got.At(i%w, i/w)) != rgba(original.At(src%3, src/3)) {
					t.Fatalf("orientation %d pixel %d", o+1, i)
				}
			}
		}
	}
}

func TestRepairFallbackPreservesEveryOtherByte(t *testing.T) {
	j := fixture(t, "progressive.jpg")
	unrelated := segment(0xe2, []byte("not an ICC profile"))
	base := inject(j, exif(6, binary.LittleEndian), unrelated, segment(0xfe, []byte{0, 255, 216, 255, 217}))
	for _, p := range [][]byte{[]byte("malformed"), fixture(t, "p3.icc")} {
		input := tagged(base, p)
		r := call(t, input, ProfileFree, ProfileRemoved)
		if r.MediaType != "image/jpeg" || !bytes.Equal(r.Data, base) {
			t.Fatal("ICC-only removal changed other bytes")
		}
	}
	r := call(t, tagged(base, []byte("malformed")), PreferColorManaged, ProfileRemoved)
	if !bytes.Equal(r.Data, base) {
		t.Fatal("automatic fallback changed non-ICC bytes")
	}
	call(t, tagged(j, fixture(t, "p3.icc")), PreferColorManaged, ColorManaged)
}

func TestRepairMultipartICC(t *testing.T) {
	j, p := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	n := len(p) / 2
	input := inject(j, iccChunk(p[n:], 2, 2), segment(0xe2, []byte("other")), iccChunk(p[:n], 1, 2))
	a := decodedPNG(t, call(t, input, PreferColorManaged, ColorManaged))
	b := decodedPNG(t, call(t, tagged(j, p), PreferColorManaged, ColorManaged))
	comparePixels(t, a, b, 0)
	for _, parts := range [][][]byte{{iccChunk(p, 1, 2)}, {iccChunk(p, 1, 1), iccChunk(p, 1, 1)}, {iccChunk(p, 0, 1)}, {iccChunk(p[:n], 1, 2), iccChunk(p[n:], 2, 3)}, {segment(0xe2, []byte("ICC_PROFILE\x00"))}} {
		r := call(t, inject(j, parts...), PreferColorManaged, ProfileRemoved)
		if !bytes.Equal(r.Data, j) {
			t.Fatal("bad chunks not safely stripped")
		}
	}
}

func TestRepairMalformedEXIFFallsBack(t *testing.T) {
	j, p := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	bad := [][]byte{exif(0, binary.LittleEndian), exif(9, binary.BigEndian), segment(0xe1, []byte("Exif\x00\x00bad"))}
	corrupt := exif(6, binary.LittleEndian)
	binary.LittleEndian.PutUint32(corrupt[14:], 0xfffffff0)
	bad = append(bad, corrupt)
	for _, e := range bad {
		base := inject(j, e)
		r := call(t, tagged(base, p), PreferColorManaged, ProfileRemoved)
		if !bytes.Equal(r.Data, base) {
			t.Fatal("malformed EXIF changed")
		}
		call(t, base, PreferColorManaged, Unchanged)
	}
	base := inject(j, exif(1, binary.LittleEndian), exif(8, binary.LittleEndian))
	r := call(t, tagged(base, p), PreferColorManaged, ProfileRemoved)
	if !bytes.Equal(r.Data, base) {
		t.Fatal("ambiguous orientation changed")
	}
}

func TestRepairRefusesMultipictureAndUnsafeStructure(t *testing.T) {
	j, p := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	cases := [][]byte{nil, []byte("not a jpeg"), j[:len(j)-1], {255, 216, 255, 226, 0, 1, 255, 217}, {255, 216, 255, 226, 255, 255}, append(bytes.Clone(j), j...), append(bytes.Clone(j), []byte("trailing data")...), inject(j, segment(0xe2, []byte("MPF\x00anything"))), inject(j, segment(0xe2, []byte("MPF\x00")))}
	for _, b := range cases {
		if len(b) >= 2 && b[0] == 255 && b[1] == 216 {
			b = tagged(b, p)
		}
		for _, mode := range []Mode{PreferColorManaged, ProfileFree} {
			call(t, b, mode, Unchanged)
		}
	}
}

func profileTag(t testing.TB, p []byte, name string) (entry, off, size int) {
	t.Helper()
	n := int(binary.BigEndian.Uint32(p[128:]))
	for i := range n {
		e := 132 + 12*i
		if string(p[e:e+4]) == name {
			return e, int(binary.BigEndian.Uint32(p[e+4:])), int(binary.BigEndian.Uint32(p[e+8:]))
		}
	}
	t.Fatalf("missing tag %s", name)
	return
}
func TestRepairRejectsInvalidAndUnsupportedICC(t *testing.T) {
	j, source := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	mutations := map[string]func([]byte){
		"nonRGB": func(p []byte) { copy(p[16:], "CMYK") }, "nonXYZ": func(p []byte) { copy(p[20:], "Lab ") }, "deviceLink": func(p []byte) { copy(p[12:], "link") }, "badSignature": func(p []byte) { copy(p[36:], "oops") }, "wrongSize": func(p []byte) { binary.BigEndian.PutUint32(p, uint32(len(p)+1)) },
		"duplicate": func(p []byte) { copy(p[144:148], p[132:136]) },
		"overlap": func(p []byte) {
			a, o, _ := profileTag(t, p, "rXYZ")
			_, q, _ := profileTag(t, p, "gXYZ")
			_ = o
			binary.BigEndian.PutUint32(p[a+4:], uint32(q+4))
		},
		"outOfBounds":        func(p []byte) { binary.BigEndian.PutUint32(p[136:], 0xfffffffc) },
		"LUT":                func(p []byte) { e, _, _ := profileTag(t, p, "desc"); copy(p[e:], "A2B0") },
		"badGamma":           func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint32(p[o+12:], 0) },
		"negativeSlope":      func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint32(p[o+16:], 0xffff0000) },
		"discontinuousCurve": func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint32(p[o+24:], 0x10000) },
		"negativeMatrix":     func(p []byte) { _, o, _ := profileTag(t, p, "rXYZ"); binary.BigEndian.PutUint32(p[o+8:], 0xff000000) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := bytes.Clone(source)
			mutate(p)
			r := call(t, tagged(j, p), PreferColorManaged, ProfileRemoved)
			if !bytes.Equal(r.Data, j) {
				t.Fatal("fallback changed pixels")
			}
		})
	}
}

func TestRepairBudgets(t *testing.T) {
	j, p := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	large := make([]byte, 32*1024*1024+1)
	copy(large, tagged(j, p))
	call(t, large, PreferColorManaged, Unchanged)
	call(t, large, ProfileFree, Unchanged)
	// A complete marker stream with oversized dimensions must take metadata fallback before decode/allocation.
	oversized := bytes.Clone(j)
	for i := 2; i+9 < len(oversized); i++ {
		if oversized[i] == 255 && oversized[i+1] == 0xc0 {
			binary.BigEndian.PutUint16(oversized[i+5:], 4001)
			binary.BigEndian.PutUint16(oversized[i+7:], 4000)
			break
		}
	}
	r := call(t, tagged(oversized, p), PreferColorManaged, ProfileRemoved)
	if !bytes.Equal(r.Data, oversized) {
		t.Fatal("pixel budget fallback changed bytes")
	}
	// ICC assembly may exceed the budget, but stripping never needs to assemble it.
	var parts [][]byte
	for i := range 18 {
		parts = append(parts, iccChunk(make([]byte, 60000), byte(i+1), 18))
	}
	r = call(t, inject(j, parts...), PreferColorManaged, ProfileRemoved)
	if !bytes.Equal(r.Data, j) {
		t.Fatal("ICC budget fallback changed bytes")
	}
}

type cancelAfterChecks struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestRepairCancellation(t *testing.T) {
	j := tagged(fixture(t, "patches.jpg"), fixture(t, "p3.icc"))
	// Cancel at every cooperative boundary reached by a complete repair. No timing race.
	completed := false
	for n := 1; n < 2000; n++ {
		ctx, cancel := context.WithCancel(context.Background())
		c := &cancelAfterChecks{ctx, cancel, n}
		r, e := RepairJPEG(c, j, PreferColorManaged)
		cancel()
		if e == nil {
			if r.Method != ColorManaged {
				t.Fatalf("method %v", r.Method)
			}
			completed = true
			break
		}
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("error = %v", e)
		}
		if r.Method != Unchanged {
			t.Fatal("partial canceled result")
		}
	}
	if !completed {
		t.Fatal("repair failed to complete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := RepairJPEG(ctx, j, ProfileFree)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func FuzzRepairJPEG(f *testing.F) {
	j, p := fixture(f, "patches.jpg"), fixture(f, "p3.icc")
	for _, b := range [][]byte{j, tagged(j, p), tagged(fixture(f, "progressive.jpg"), p), inject(j, exif(8, binary.LittleEndian)), inject(j, segment(0xe2, []byte("MPF\x00"))), {255, 216, 255, 226, 0, 1}, nil} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 2*1024*1024 {
			return
		}
		original := bytes.Clone(b)
		for _, mode := range []Mode{PreferColorManaged, ProfileFree} {
			r, e := RepairJPEG(context.Background(), b, mode)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(b, original) {
				t.Fatal("mutated input")
			}
			switch r.Method {
			case Unchanged:
				if !bytes.Equal(r.Data, b) {
					t.Fatal("changed unchanged bytes")
				}
			case ColorManaged:
				if r.MediaType != "image/png" {
					t.Fatal(r.MediaType)
				}
				if _, e := png.Decode(bytes.NewReader(r.Data)); e != nil {
					t.Fatal(e)
				}
			case ProfileRemoved:
				if r.MediaType != "image/jpeg" || len(r.Data) >= len(b) {
					t.Fatal("invalid fallback")
				}
			default:
				t.Fatal(r.Method)
			}
		}
	})
}
