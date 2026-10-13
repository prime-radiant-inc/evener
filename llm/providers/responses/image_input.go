package responses

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"

	// Register JPEG decoding at the shared image normalization boundary.
	_ "image/jpeg"
	"image/png"
	"strings"

	// Register additional raster decoders before enforcing OpenAI's wire formats.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"primeradiant.com/evener/llm"
)

func toolResultHasProviderImage(result *llm.ToolResultData) bool {
	if result == nil || len(result.ImageData) == 0 {
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(result.ImageMediaType))
	return mediaType == "" || strings.HasPrefix(mediaType, "image/")
}

// normalizeImageInput re-encodes data so its media type is one OpenAI's image
// input accepts on the wire (image/png, image/jpeg, image/gif, image/webp); a
// BMP or TIFF becomes PNG. An animated GIF is flattened to its first frame,
// the single still the wire carries. claimedMediaType appears only in the
// error text.
func normalizeImageInput(data []byte, claimedMediaType string) ([]byte, string, error) {
	if _, format, err := image.Decode(bytes.NewReader(data)); err == nil && format == "gif" {
		if frames, derr := gif.DecodeAll(bytes.NewReader(data)); derr == nil && len(frames.Image) > 1 {
			return encodeImageInputAsPNG(frames.Image[0])
		}
	}
	normalized, mediaType, err := llm.NormalizeImageBlock(data)
	if err != nil {
		return nil, "", fmt.Errorf("invalid OpenAI image input %q: %w", claimedMediaType, err)
	}
	return normalized, mediaType, nil
}

func encodeImageInputAsPNG(decoded image.Image) ([]byte, string, error) {
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, decoded); err != nil {
		return nil, "", fmt.Errorf("encode OpenAI image input as PNG: %w", err)
	}
	return normalized.Bytes(), "image/png", nil
}
