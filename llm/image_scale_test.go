package llm

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand"
	"testing"

	"golang.org/x/image/bmp"
)

// imageScaleEncodePNG encodes img as PNG or fails the test.
func imageScaleEncodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return buf.Bytes()
}

// imageScalePatternedImage returns an image with two solid halves, so a
// downscale has a visible edge to preserve but compresses to a small PNG.
func imageScalePatternedImage(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x < width/2 {
				img.SetRGBA(x, y, color.RGBA{R: 0x20, G: 0x80, B: 0xC0, A: 0xFF})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0xC0, G: 0x40, B: 0x20, A: 0xFF})
			}
		}
	}
	return img
}

// imageScaleNoisyGray returns deterministic gray noise: a PNG of it exceeds a
// small byte cap, while JPEG compresses it well enough to fit one.
func imageScaleNoisyGray(width, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	rnd := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = byte(rnd.Uint32())
	}
	return img
}

// imageScaleAnimatedGIF builds a multi-frame GIF whose frames carry different
// pixels, so it is genuinely animated.
func imageScaleAnimatedGIF(t *testing.T, size, frames int) []byte {
	t.Helper()
	palette := color.Palette{
		color.RGBA{A: 0xFF},
		color.RGBA{R: 0xFF, A: 0xFF},
		color.RGBA{G: 0xFF, A: 0xFF},
		color.RGBA{B: 0xFF, A: 0xFF},
	}
	anim := &gif.GIF{}
	for frame := 0; frame < frames; frame++ {
		paletted := image.NewPaletted(image.Rect(0, 0, size, size), palette)
		for i := range paletted.Pix {
			paletted.Pix[i] = uint8((i + frame) % len(palette))
		}
		anim.Image = append(anim.Image, paletted)
		anim.Delay = append(anim.Delay, 0)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("encode animated GIF fixture: %v", err)
	}
	return buf.Bytes()
}

// imageScaleAppendPNGChunk appends one PNG chunk, CRC included.
func imageScaleAppendPNGChunk(buf *bytes.Buffer, kind string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	buf.Write(length[:])
	buf.WriteString(kind)
	buf.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(kind))
	_, _ = crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	buf.Write(sum[:])
}

// imageScaleCraftGrayPNG hand-builds a valid 8-bit grayscale PNG of the given
// dimensions without allocating the decoded canvas. All pixels are zero, which
// deflate reduces to a small IDAT even at 10000x10000.
func imageScaleCraftGrayPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var idat bytes.Buffer
	zw, err := zlib.NewWriterLevel(&idat, zlib.BestSpeed)
	if err != nil {
		t.Fatalf("zlib writer: %v", err)
	}
	row := make([]byte, 1+width) // filter byte 0 followed by zero pixels
	for y := 0; y < height; y++ {
		if _, err := zw.Write(row); err != nil {
			t.Fatalf("zlib write row %d: %v", y, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}

	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	var ihdr bytes.Buffer
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], uint32(width))
	ihdr.Write(word[:])
	binary.BigEndian.PutUint32(word[:], uint32(height))
	ihdr.Write(word[:])
	ihdr.Write([]byte{8, 0, 0, 0, 0}) // bit depth 8, grayscale, deflate, adaptive filter, no interlace
	imageScaleAppendPNGChunk(&out, "IHDR", ihdr.Bytes())
	imageScaleAppendPNGChunk(&out, "IDAT", idat.Bytes())
	imageScaleAppendPNGChunk(&out, "IEND", nil)
	return out.Bytes()
}

// A small PNG the wire already accepts is returned byte-for-byte. Re-encoding
// it would cost quality for no benefit.
func TestPrepareImageForModelFastPathUnchanged(t *testing.T) {
	t.Parallel()
	data := imageScaleEncodePNG(t, imageScalePatternedImage(400, 300))
	got, err := PrepareImageForModel(data)
	if err != nil {
		t.Fatalf("PrepareImageForModel: %v", err)
	}
	if !bytes.Equal(got.Data, data) {
		t.Fatalf("fast-path bytes changed: got %d bytes, want the original %d byte-identical", len(got.Data), len(data))
	}
	if got.MediaType != "image/png" {
		t.Fatalf("fast-path media type = %q, want image/png", got.MediaType)
	}
	if got.Scaled {
		t.Fatal("fast path reported Scaled = true, want false")
	}
	if got.Width != 400 || got.Height != 300 || got.OriginalWidth != 400 || got.OriginalHeight != 300 {
		t.Fatalf("fast-path dimensions = %dx%d (original %dx%d), want 400x300 for both", got.Width, got.Height, got.OriginalWidth, got.OriginalHeight)
	}
}

// A PNG wider than MaxModelImageDimension is downscaled proportionally, and
// the bytes that result are a decodable wire format within the byte cap.
func TestPrepareImageForModelDownscalesOversizedDimension(t *testing.T) {
	t.Parallel()
	data := imageScaleEncodePNG(t, imageScalePatternedImage(3000, 1500))
	got, err := PrepareImageForModel(data)
	if err != nil {
		t.Fatalf("PrepareImageForModel: %v", err)
	}
	if !got.Scaled {
		t.Fatal("a 3000x1500 image reported Scaled = false")
	}
	if got.OriginalWidth != 3000 || got.OriginalHeight != 1500 {
		t.Fatalf("original dimensions = %dx%d, want 3000x1500", got.OriginalWidth, got.OriginalHeight)
	}
	if got.Width != 2000 || got.Height != 1000 {
		t.Fatalf("displayed dimensions = %dx%d, want 2000x1000 (proportional)", got.Width, got.Height)
	}
	if len(got.Data) > MaxModelImageBytes {
		t.Fatalf("scaled bytes = %d, want <= %d", len(got.Data), MaxModelImageBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("scaled bytes do not decode: %v", err)
	}
	if cfg.Width != 2000 || cfg.Height != 1000 {
		t.Fatalf("scaled bytes decode to %dx%d, want 2000x1000", cfg.Width, cfg.Height)
	}
	if ImageBlockMediaType(got.MediaType) == "" {
		t.Fatalf("scaled media type = %q, not accepted by an image block", got.MediaType)
	}
}

// An image within the dimension limit but over the byte cap is re-encoded
// below the cap. Not parallel: it lowers the package-level cap on purpose.
func TestPrepareImageForModelCompressesOversizedBytes(t *testing.T) {
	const cap = 100_000
	old := MaxModelImageBytes
	MaxModelImageBytes = cap
	defer func() { MaxModelImageBytes = old }()

	data := imageScaleEncodePNG(t, imageScaleNoisyGray(1000, 1000))
	if len(data) <= cap {
		t.Fatalf("fixture PNG is %d bytes, want > %d so the byte cap is the trigger", len(data), cap)
	}
	got, err := PrepareImageForModel(data)
	if err != nil {
		t.Fatalf("PrepareImageForModel: %v", err)
	}
	// Scaled tracks pixel dimensions only: this fixture is re-encoded for the
	// byte cap and may or may not be shrunk, so the flag must agree with the
	// dimensions that came back.
	wantScaled := got.Width != got.OriginalWidth || got.Height != got.OriginalHeight
	if got.Scaled != wantScaled {
		t.Fatalf("Scaled = %v, want %v for %dx%d -> %dx%d", got.Scaled, wantScaled,
			got.OriginalWidth, got.OriginalHeight, got.Width, got.Height)
	}
	if len(got.Data) > cap {
		t.Fatalf("compressed bytes = %d, want <= %d", len(got.Data), cap)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(got.Data)); err != nil {
		t.Fatalf("compressed bytes do not decode: %v", err)
	}
	if ImageBlockMediaType(got.MediaType) == "" {
		t.Fatalf("compressed media type = %q, not accepted by an image block", got.MediaType)
	}
}

// A PNG over the byte cap can be recompressed to JPEG at its original pixel
// dimensions. That is a byte re-encode, not a downscale, so Scaled stays false.
// Not parallel: it lowers the package-level cap to the fixture's own size.
func TestPrepareImageForModelByteReencodeKeepsScaledFalse(t *testing.T) {
	data := imageScaleEncodePNG(t, imageScaleNoisyGray(400, 400))
	old := MaxModelImageBytes
	// The same image re-encoded as PNG cannot fit this cap, so the byte ladder
	// must fall through to JPEG without resizing.
	MaxModelImageBytes = len(data) - 1
	defer func() { MaxModelImageBytes = old }()

	got, err := PrepareImageForModel(data)
	if err != nil {
		t.Fatalf("PrepareImageForModel: %v", err)
	}
	if got.Width != 400 || got.Height != 400 {
		t.Fatalf("byte re-encode resized to %dx%d, want the original 400x400", got.Width, got.Height)
	}
	if got.Scaled {
		t.Fatal("a same-dimension PNG->JPEG re-encode reported Scaled = true")
	}
	if got.MediaType != "image/jpeg" {
		t.Fatalf("byte re-encode media type = %q, want image/jpeg", got.MediaType)
	}
	if len(got.Data) > MaxModelImageBytes {
		t.Fatalf("re-encoded bytes = %d, want <= %d", len(got.Data), MaxModelImageBytes)
	}
}

// BMP is decodable but not accepted on the wire, so even a small BMP is
// converted to a wire format.
func TestPrepareImageForModelConvertsBMPToWireFormat(t *testing.T) {
	t.Parallel()
	var bitmap bytes.Buffer
	if err := bmp.Encode(&bitmap, imageScalePatternedImage(100, 100)); err != nil {
		t.Fatalf("encode BMP fixture: %v", err)
	}
	got, err := PrepareImageForModel(bitmap.Bytes())
	if err != nil {
		t.Fatalf("PrepareImageForModel: %v", err)
	}
	if ImageBlockMediaType(got.MediaType) == "" {
		t.Fatalf("converted media type = %q, not accepted by an image block", got.MediaType)
	}
	if got.MediaType != "image/png" {
		t.Fatalf("converted media type = %q, want image/png", got.MediaType)
	}
	if got.Scaled {
		t.Fatalf("a 100x100 BMP re-encoded at the same dimensions reported Scaled = true")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("converted bytes do not decode: %v", err)
	}
	if cfg.Width != 100 || cfg.Height != 100 {
		t.Fatalf("converted bytes decode to %dx%d, want 100x100", cfg.Width, cfg.Height)
	}
}

// An animated GIF keeps all its frames when it fits the byte cap, and is
// refused rather than flattened to frame 0 when it does not. Not parallel: the
// over-cap case lowers the package-level cap on purpose.
func TestPrepareImageForModelAnimatedGIF(t *testing.T) {
	data := imageScaleAnimatedGIF(t, 8, 2)
	got, err := PrepareImageForModel(data)
	if err != nil {
		t.Fatalf("PrepareImageForModel: %v", err)
	}
	if !bytes.Equal(got.Data, data) {
		t.Fatalf("animated GIF bytes changed: got %d bytes, want the original %d", len(got.Data), len(data))
	}
	if got.Scaled {
		t.Fatal("animated GIF under the cap reported Scaled = true")
	}
	if got.MediaType != "image/gif" {
		t.Fatalf("animated GIF media type = %q, want image/gif", got.MediaType)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("returned GIF does not decode: %v", err)
	}
	if len(decoded.Image) != 2 {
		t.Fatalf("returned GIF has %d frames, want all 2", len(decoded.Image))
	}

	old := MaxModelImageBytes
	MaxModelImageBytes = len(data) - 1
	defer func() { MaxModelImageBytes = old }()
	if _, err := PrepareImageForModel(data); err == nil {
		t.Fatal("animated GIF over the byte cap was accepted; want an error rather than flattening it to one frame")
	}
}

// A PNG header declaring 10000x10000 (100 MP, above MaxScalableImagePixels) is
// refused from image.DecodeConfig alone. The fixture is a fully valid PNG, so
// an implementation that skipped the header check would decode it and succeed.
func TestPrepareImageForModelRejectsDecompressionBomb(t *testing.T) {
	t.Parallel()
	// Prove the hand-built chunk writer produces a fully decodable PNG before
	// relying on it for the 100 MP header.
	small := imageScaleCraftGrayPNG(t, 2, 2)
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(small)); err != nil || format != "png" || cfg.Width != 2 || cfg.Height != 2 {
		t.Fatalf("2x2 fixture header = %dx%d format %q err %v, want 2x2 png", cfg.Width, cfg.Height, format, err)
	}
	if _, format, err := image.Decode(bytes.NewReader(small)); err != nil || format != "png" {
		t.Fatalf("2x2 fixture does not fully decode: format %q err %v", format, err)
	}

	bomb := imageScaleCraftGrayPNG(t, 10000, 10000)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(bomb))
	if err != nil {
		t.Fatalf("100 MP fixture header does not decode: %v", err)
	}
	if cfg.Width != 10000 || cfg.Height != 10000 {
		t.Fatalf("100 MP fixture declares %dx%d, want 10000x10000", cfg.Width, cfg.Height)
	}
	if _, err := PrepareImageForModel(bomb); err == nil {
		t.Fatalf("a %dx%d PNG was accepted; want a pixel-budget error before decode", cfg.Width, cfg.Height)
	}
}
