package imagerepair

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"testing"
)

// Every curve encoding has its own independently interpreted LittleCMS profile.
func TestICCCurveEncodingsAgainstLittleCMS(t *testing.T) {
	var refs struct {
		Profiles map[string][]struct{ Input, Output [3]uint8 }
	}
	if e := json.Unmarshal(fixture(t, "samples.json"), &refs); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"identity", "gamma", "sampled", "para0", "para1", "para2", "para3", "para4"} {
		t.Run(name, func(t *testing.T) {
			if len(refs.Profiles[name]) == 0 {
				t.Fatal("missing independent color samples")
			}
			transform, ok := parseICC(context.Background(), fixture(t, name+".icc"))
			if !ok {
				t.Fatal("unsupported curve fixture")
			}
			compareColorSamples(t, transform, refs.Profiles[name])
		})
	}
}

func TestICCAdditionalSafety(t *testing.T) {
	j, base := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	cases := map[string]func([]byte){
		"v2":               func(p []byte) { p[8] = 2 },
		"PCSwhite":         func(p []byte) { binary.BigEndian.PutUint32(p[68:], 0) },
		"tagCount":         func(p []byte) { binary.BigEndian.PutUint32(p[128:], 0xffffffff) },
		"tagInsideTable":   func(p []byte) { binary.BigEndian.PutUint32(p[136:], 132) },
		"tagUnaligned":     func(p []byte) { e, o, _ := profileTag(t, p, "rXYZ"); binary.BigEndian.PutUint32(p[e+4:], uint32(o+1)) },
		"missingTRC":       func(p []byte) { e, _, _ := profileTag(t, p, "rTRC"); copy(p[e:], "junk") },
		"TRCReserved":      func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); p[o+4] = 1 },
		"unknownCurve":     func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint16(p[o+8:], 5) },
		"badParamSize":     func(p []byte) { e, _, n := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint32(p[e+8:], uint32(n-1)) },
		"invalidPowDomain": func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint32(p[o+20:], 0xffff0000) },
		"outOfRangeCurve":  func(p []byte) { _, o, _ := profileTag(t, p, "rTRC"); binary.BigEndian.PutUint32(p[o+16:], 0x30000) },
		"singularMatrix": func(p []byte) {
			_, r, _ := profileTag(t, p, "rXYZ")
			_, g, _ := profileTag(t, p, "gXYZ")
			copy(p[r:r+20], p[g:g+20])
		},
		"badCHAD":     func(p []byte) { _, o, _ := profileTag(t, p, "chad"); clear(p[o+8 : o+44]) },
		"badWhiteTag": func(p []byte) { _, o, _ := profileTag(t, p, "wtpt"); binary.BigEndian.PutUint32(p[o+8:], 0) },
	}
	for _, name := range []string{"A2B1", "B2A0", "D2B0", "B2D3"} {
		cases[name] = func(p []byte) { e, _, _ := profileTag(t, p, "desc"); copy(p[e:], name) }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := bytes.Clone(base)
			mutate(p)
			r := call(t, tagged(j, p), PreferColorManaged, ProfileRemoved)
			if !bytes.Equal(r.Data, j) {
				t.Fatal("fallback bytes changed")
			}
		})
	}
	p := fixture(t, "sampled.icc")
	_, o, _ := profileTag(t, p, "rTRC")
	binary.BigEndian.PutUint16(p[o+12+4:], 65535)
	call(t, tagged(j, p), PreferColorManaged, ProfileRemoved)
	// Metadata names must not choose the transform. Rename this P3 profile sRGB
	// without changing the valid mluc encoding or its matrix/TRC payloads.
	p = bytes.Clone(base)
	_, o, _ = profileTag(t, p, "desc")
	for i, r := range "sRGB profile" {
		binary.BigEndian.PutUint16(p[o+28+2*i:], uint16(r))
	}
	a := decodedPNG(t, call(t, tagged(j, p), PreferColorManaged, ColorManaged))
	b := decodedPNG(t, call(t, tagged(j, base), PreferColorManaged, ColorManaged))
	comparePixels(t, a, b, 0)
}

func TestRepairUnsupportedPixelModel(t *testing.T) {
	j := fixture(t, "cmyk.jpg")
	call(t, j, PreferColorManaged, Unchanged)
	r := call(t, tagged(j, fixture(t, "p3.icc")), PreferColorManaged, ProfileRemoved)
	if !bytes.Equal(r.Data, j) {
		t.Fatal("CMYK changed")
	}
}

func TestRepairProgressiveAndLateMPF(t *testing.T) {
	j := fixture(t, "progressive.jpg")
	original, err := jpeg.Decode(bytes.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	comparePixels(t, decodedPNG(t, call(t, j, PreferColorManaged, ColorManaged)), original, 0)
	// APP2 can appear after the last scan, not only before the first SOS.
	late := append(bytes.Clone(j[:len(j)-2]), segment(0xe2, []byte("MPF\x00"))...)
	late = append(late, j[len(j)-2:]...)
	late = tagged(late, fixture(t, "p3.icc"))
	for _, mode := range []Mode{PreferColorManaged, ProfileFree} {
		call(t, late, mode, Unchanged)
	}
}

func manyICC(j, p []byte) []byte {
	var parts [][]byte
	const size = 60000
	n := (len(p) + size - 1) / size
	for i := range n {
		parts = append(parts, iccChunk(p[i*size:min((i+1)*size, len(p))], byte(i+1), byte(n)))
	}
	return inject(j, parts...)
}
func padJPEG(j []byte, total int) []byte {
	out := make([]byte, 0, total)
	out = append(out, j[:2]...)
	for remaining := total - len(j); remaining > 0; {
		n := min(remaining, 65537)
		if rest := remaining - n; rest > 0 && rest < 4 {
			n -= 4
		}
		out = append(out, segment(0xfe, make([]byte, n-4))...)
		remaining -= n
	}
	return append(out, j[2:]...)
}
func TestRepairExactEncodedAndICCBudgets(t *testing.T) {
	j, p := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	atLimit := padJPEG(tagged(j, p), 32<<20)
	r := call(t, atLimit, ProfileFree, ProfileRemoved)
	want := padJPEG(j, (32<<20)-len(iccChunk(p, 1, 1)))
	if !bytes.Equal(r.Data, want) {
		t.Fatal("exact JPEG budget removal changed bytes")
	}
	above := padJPEG(tagged(j, p), (32<<20)+1)
	call(t, above, ProfileFree, Unchanged)
	for _, size := range []int{1 << 20, (1 << 20) + 1} {
		profile := make([]byte, size)
		copy(profile, p)
		binary.BigEndian.PutUint32(profile, uint32(size))
		want := ColorManaged
		if size > 1<<20 {
			want = ProfileRemoved
		}
		r := call(t, manyICC(j, profile), PreferColorManaged, want)
		if want == ProfileRemoved && !bytes.Equal(r.Data, j) {
			t.Fatal("ICC budget fallback changed bytes")
		}
	}
}
func TestRepairExactPixelBudget(t *testing.T) {
	im := image.NewGray(image.Rect(0, 0, 4000, 4000))
	var buf bytes.Buffer
	if e := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 75}); e != nil {
		t.Fatal(e)
	}
	r := call(t, buf.Bytes(), PreferColorManaged, ColorManaged)
	cfg, _, e := image.DecodeConfig(bytes.NewReader(r.Data))
	if e != nil || cfg.Width != 4000 || cfg.Height != 4000 {
		t.Fatalf("config %v, %v", cfg, e)
	}
}
func TestPNGOutputBudgetAndCancellation(t *testing.T) {
	w := pngWriter{ctx: context.Background()}
	chunk := make([]byte, 1<<20)
	for i := range 64 {
		n, e := w.Write(chunk)
		if e != nil || n != len(chunk) {
			t.Fatalf("write %d: %d %v", i, n, e)
		}
	}
	if n, e := w.Write([]byte{0}); n != 0 || !errors.Is(e, errPNGBudget) {
		t.Fatalf("limit write %d %v", n, e)
	}
	if w.buf.Len() != 64<<20 {
		t.Fatal("oversized output")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.ctx = ctx
	if n, e := w.Write(nil); n != 0 || !errors.Is(e, context.Canceled) {
		t.Fatalf("canceled write %d %v", n, e)
	}
}

func TestRepairInvalidEXIFStructures(t *testing.T) {
	j, p := fixture(t, "patches.jpg"), fixture(t, "p3.icc")
	for _, mutate := range []func([]byte){func(e []byte) { copy(e[10:], "ZZ") }, func(e []byte) { binary.LittleEndian.PutUint16(e[12:], 41) }, func(e []byte) { binary.LittleEndian.PutUint16(e[18:], 65535) }, func(e []byte) { binary.LittleEndian.PutUint16(e[22:], 4) }, func(e []byte) { binary.LittleEndian.PutUint32(e[24:], 2) }} {
		e := exif(6, binary.LittleEndian)
		mutate(e)
		base := inject(j, e)
		r := call(t, tagged(base, p), PreferColorManaged, ProfileRemoved)
		if !bytes.Equal(base, r.Data) {
			t.Fatal("EXIF fallback changed bytes")
		}
	}
	// A valid empty IFD0 has no orientation, so defaults to identity.
	e := exif(6, binary.LittleEndian)
	binary.LittleEndian.PutUint16(e[18:], 0)
	clear(e[20:24]) // No next IFD.
	a := decodedPNG(t, call(t, inject(j, e), PreferColorManaged, ColorManaged))
	b := decodedPNG(t, call(t, j, PreferColorManaged, ColorManaged))
	comparePixels(t, a, b, 0)
}
func TestRepairOwnsChangedOutput(t *testing.T) {
	for _, mode := range []Mode{PreferColorManaged, ProfileFree} {
		input := tagged(fixture(t, "patches.jpg"), fixture(t, "p3.icc"))
		before := bytes.Clone(input)
		r, e := RepairJPEG(context.Background(), input, mode)
		if e != nil {
			t.Fatal(e)
		}
		clear(r.Data)
		if !bytes.Equal(input, before) {
			t.Fatal("output aliases source")
		}
	}
}

func FuzzICC(f *testing.F) {
	for _, name := range []string{"p3.icc", "srgb.icc", "sampled.icc", "para4.icc"} {
		f.Add(fixture(f, name))
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, p []byte) {
		if len(p) > 1<<20 {
			return
		}
		before := bytes.Clone(p)
		tr, ok := parseICC(context.Background(), p)
		if !bytes.Equal(p, before) {
			t.Fatal("profile mutated")
		}
		if ok {
			for _, rgb := range [][3]uint8{{0, 0, 0}, {255, 255, 255}, {128, 70, 250}} {
				if tr.rgb(rgb[0], rgb[1], rgb[2]).A != 255 {
					t.Fatal("nonopaque conversion")
				}
			}
		}
	})
}
func FuzzOrientation(f *testing.F) {
	f.Add(exif(8, binary.LittleEndian)[10:])
	f.Add(exif(7, binary.BigEndian)[10:])
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		o, ok := parseOrientation(b)
		if ok && (o < 1 || o > 8) {
			t.Fatalf("orientation %d", o)
		}
	})
}
