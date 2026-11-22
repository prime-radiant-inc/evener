package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// readFileMediaPNG builds the oversized image payload a read_file image result
// carries: the [image: ...] envelope execenv produces followed by base64 PNG.
func readFileMediaPNG(t *testing.T, width, height int) ([]byte, string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x < width/2 {
				img.SetRGBA(x, y, color.RGBA{R: 0x20, G: 0xA0, B: 0x40, A: 0xFF})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0xA0, G: 0x20, B: 0x40, A: 0xFF})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return buf.Bytes(), "[image: big.png]\n" + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// read_file downscales a 3000x1500 PNG to 2000x1000 before returning it to the
// model, and its text carries the original and displayed dimensions plus the
// factor that maps displayed coordinates back to the original.
func TestReadFileDownscalesOversizedImageAndAnnotatesCoordinates(t *testing.T) {
	t.Parallel()
	original, payload := readFileMediaPNG(t, 3000, 1500)
	sess := newSession(t, withEnv(&w3sub_readFileEnv{output: payload}))
	args, err := json.Marshal(map[string]any{"file_path": "big.png"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID: "read-big-image", Name: "read_file", Arguments: args, Type: "function",
	}, "")
	if res.IsError {
		t.Fatalf("read_file failed: %q", res.Output)
	}
	if bytes.Equal(res.ImageData, original) {
		t.Fatal("read_file returned the original 3000x1500 bytes; want downscaled bytes")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(res.ImageData))
	if err != nil {
		t.Fatalf("returned image bytes do not decode: %v", err)
	}
	if cfg.Width != 2000 || cfg.Height != 1000 {
		t.Fatalf("returned image decodes to %dx%d, want 2000x1000", cfg.Width, cfg.Height)
	}
	if llm.ImageBlockMediaType(res.ImageMediaType) == "" {
		t.Fatalf("returned media type = %q, not accepted by an image block", res.ImageMediaType)
	}
	for _, want := range []string{"3000x1500", "2000x1000", "1.50"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("result text = %q, want it to carry %q (original, displayed, scale factor)", res.Output, want)
		}
	}
}
