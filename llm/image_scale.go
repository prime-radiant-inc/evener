package llm

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
)

// Bounds read_file applies to an image before it enters model history. They are
// package-level vars rather than consts so tests can exercise the byte and
// pixel ladders without multi-megabyte fixtures.
var (
	// MaxModelImageDimension is the largest edge, in pixels, an image may have
	// when it is handed to a model. Anything larger is downscaled
	// proportionally.
	MaxModelImageDimension = 2000

	// MaxModelImageBytes bounds an image's encoded size. Base64 inflates the
	// payload by a third, so 4.5 MB keeps the wire form under 6 MiB.
	MaxModelImageBytes = 4_500_000

	// MaxScalableImagePixels bounds the decoded canvas PrepareImageForModel may
	// allocate. 36 MP decodes to at most ~144 MB of transient RGBA and covers
	// 24 MP photos and 5K/6K screenshots while rejecting decompression bombs.
	// It deliberately differs from maxDecodedImagePixels, which bounds the
	// unscaled re-encoding NormalizeImage does.
	MaxScalableImagePixels = 36_000_000
)

// PreparedImage is image bytes that are safe to hand a model, with the
// dimensions before and after preparation. Scaled reports that the pixel
// dimensions changed: Data was downscaled, so callers that display coordinates
// derived from the new bytes need the original dimensions to map back. A
// re-encode that keeps the same dimensions (BMP converted to PNG, an over-cap
// JPEG recompressed) reports Scaled false because coordinates are unchanged.
type PreparedImage struct {
	Data           []byte
	MediaType      string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
	Scaled         bool
}

// PrepareImageForModel returns image bytes an Anthropic or OpenAI image block
// accepts on the wire, within MaxModelImageDimension and MaxModelImageBytes.
// Images that already fit pass through byte-identical with Scaled false. Larger
// images are downscaled with CatmullRom and re-encoded as PNG or JPEG until
// they fit. A declared canvas over MaxScalableImagePixels, bytes that are not a
// decodable raster, and an animated GIF over the byte cap return an error.
//
// Animated GIFs smaller than the cap are returned untouched: flattening one to
// its first frame would silently delete content the model can otherwise see.
func PrepareImageForModel(data []byte) (PreparedImage, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return PreparedImage{}, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxScalableImagePixels/cfg.Height {
		return PreparedImage{}, fmt.Errorf("decoded image %dx%d exceeds the %d-pixel budget", cfg.Width, cfg.Height, MaxScalableImagePixels)
	}
	if format == "gif" {
		anim, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return PreparedImage{}, err
		}
		if len(anim.Image) > 1 {
			if len(data) > MaxModelImageBytes {
				return PreparedImage{}, fmt.Errorf("animated GIF is %d bytes, exceeding the %d-byte image cap; it cannot be downscaled without dropping frames", len(data), MaxModelImageBytes)
			}
			return PreparedImage{
				Data:           data,
				MediaType:      ImageBlockMediaType("gif"),
				OriginalWidth:  cfg.Width,
				OriginalHeight: cfg.Height,
				Width:          cfg.Width,
				Height:         cfg.Height,
			}, nil
		}
	}

	wireType := ImageBlockMediaType(format)
	if max(cfg.Width, cfg.Height) <= MaxModelImageDimension && len(data) <= MaxModelImageBytes && wireType != "" {
		return PreparedImage{
			Data:           data,
			MediaType:      wireType,
			OriginalWidth:  cfg.Width,
			OriginalHeight: cfg.Height,
			Width:          cfg.Width,
			Height:         cfg.Height,
		}, nil
	}

	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return PreparedImage{}, err
	}
	width, height := scaledImageDimensions(cfg.Width, cfg.Height, MaxModelImageDimension)
	var display image.Image = decoded
	if width != cfg.Width || height != cfg.Height {
		display = resizeImage(decoded, width, height)
	}
	prepared := func(encoded []byte, mediaType string, width, height int) PreparedImage {
		return PreparedImage{
			Data:           encoded,
			MediaType:      mediaType,
			OriginalWidth:  cfg.Width,
			OriginalHeight: cfg.Height,
			Width:          width,
			Height:         height,
			// Only a dimension change moves coordinates; a format or byte
			// re-encode at the original size does not.
			Scaled: width != cfg.Width || height != cfg.Height,
		}
	}

	// Screenshots and images with alpha keep their edges and transparency in
	// PNG, which costs few bytes for flat UI content.
	switch format {
	case "png", "webp", "bmp", "tiff":
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, display); err != nil {
			return PreparedImage{}, fmt.Errorf("re-encode %s image as PNG: %w", format, err)
		}
		if encoded.Len() <= MaxModelImageBytes {
			return prepared(encoded.Bytes(), ImageBlockMediaType("png"), width, height), nil
		}
	}

	for _, quality := range []int{85, 75, 65} {
		if encoded, ok := encodeJPEGWithinCap(display, quality); ok {
			return prepared(encoded, ImageBlockMediaType("jpeg"), width, height), nil
		}
	}
	for range 3 {
		width = max(1, width*3/4)
		height = max(1, height*3/4)
		display = resizeImage(decoded, width, height)
		if encoded, ok := encodeJPEGWithinCap(display, 75); ok {
			return prepared(encoded, ImageBlockMediaType("jpeg"), width, height), nil
		}
	}
	return PreparedImage{}, fmt.Errorf("image %dx%d cannot be encoded within the %d-byte image cap", cfg.Width, cfg.Height, MaxModelImageBytes)
}

// scaledImageDimensions returns the largest proportional size whose longer edge
// is at most maxDimension, never below 1x1.
func scaledImageDimensions(width, height, maxDimension int) (int, int) {
	if width <= maxDimension && height <= maxDimension {
		return width, height
	}
	if width >= height {
		return maxDimension, max(1, height*maxDimension/width)
	}
	return max(1, width*maxDimension/height), maxDimension
}

// resizeImage scales src to width x height with CatmullRom, which keeps UI text
// crisp when shrinking.
func resizeImage(src image.Image, width, height int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

// encodeJPEGWithinCap encodes img at quality and reports whether the result fits
// MaxModelImageBytes.
func encodeJPEGWithinCap(img image.Image, quality int) ([]byte, bool) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, false
	}
	if encoded.Len() > MaxModelImageBytes {
		return nil, false
	}
	return encoded.Bytes(), true
}
