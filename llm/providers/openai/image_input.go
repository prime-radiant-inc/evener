package openai

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
	"primeradiant.com/serf/llm"
)

func toolResultHasProviderImage(result *llm.ToolResultData) bool {
	if result == nil || len(result.ImageData) == 0 {
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(result.ImageMediaType))
	return mediaType == "" || strings.HasPrefix(mediaType, "image/")
}

func normalizeImageInput(data []byte, claimedMediaType string) ([]byte, string, error) {
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("invalid OpenAI image input %q: %w", claimedMediaType, err)
	}

	if format == "gif" {
		decoded, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("decode OpenAI GIF input %q: %w", claimedMediaType, err)
		}
		if len(decoded.Image) > 1 {
			return encodeImageInputAsPNG(decoded.Image[0])
		}
	}
	if mediaType := openAIImageMediaType(format); mediaType != "" {
		return data, mediaType, nil
	}

	return encodeImageInputAsPNG(decoded)
}

func encodeImageInputAsPNG(decoded image.Image) ([]byte, string, error) {
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, decoded); err != nil {
		return nil, "", fmt.Errorf("encode OpenAI image input as PNG: %w", err)
	}
	return normalized.Bytes(), "image/png", nil
}

func openAIImageMediaType(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}
