package tool

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"primeradiant.com/serf/agent/schema"
)

func TestDispatchedResultAcceptsReadableRasterFormats(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{name: "PNG", mediaType: "image/png", data: encodeRasterFixture(t, "png")},
		{name: "JPEG", mediaType: "image/jpeg", data: encodeRasterFixture(t, "jpeg")},
		{name: "GIF", mediaType: "image/gif", data: encodeRasterFixture(t, "gif")},
		{name: "WebP", mediaType: "image/webp", data: decodeRasterFixture(t, "UklGRjAAAABXRUJQVlA4ICQAAABwAQCdASoBAAEAAgA0JYwCdAGIQAD++ZNsGW2xURhNJHYAAAA=")},
		{name: "BMP", mediaType: "image/bmp", data: decodeRasterFixture(t, "Qk06AAAAAAAAADYAAAAoAAAAAQAAAAEAAAABABgAAAAAAAQAAAAAAAAAAAAAAAAAAAAAAAAAMCAQAA==")},
		{name: "TIFF", mediaType: "image/tiff", data: decodeRasterFixture(t, "SUkqAAgAAAAJAAABAwABAAAAAQAAAAEBAwABAAAAAQAAAAIBAwADAAAAegAAAAMBAwABAAAAAQAAAAYBAwABAAAAAgAAABEBBAABAAAAgAAAABUBAwABAAAAAwAAABYBBAABAAAAAQAAABcBBAABAAAAAwAAAAAAAAAIAAgACAAQIDA=")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := dispatchedResult("image_fixture", "call", schema.ToolOutputLimit{}, ImageResult{
				Text:      "read image",
				Data:      test.data,
				MediaType: test.mediaType,
			}, nil)
			if result.IsError {
				t.Fatalf("valid raster was rejected: %s", result.FullOutput)
			}
			if !bytes.Equal(result.ImageData, test.data) || result.ImageMediaType != test.mediaType {
				t.Fatalf("image result = data %d bytes, media type %q", len(result.ImageData), result.ImageMediaType)
			}
		})
	}
}

func encodeRasterFixture(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	var data bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&data, img)
	case "jpeg":
		err = jpeg.Encode(&data, img, nil)
	case "gif":
		err = gif.Encode(&data, img, nil)
	default:
		t.Fatalf("unsupported raster fixture format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s fixture: %v", format, err)
	}
	return data.Bytes()
}

func decodeRasterFixture(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode raster fixture: %v", err)
	}
	return data
}
