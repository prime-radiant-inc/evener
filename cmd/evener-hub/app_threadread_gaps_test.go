package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestOutputImageDescriptorKeySHA covers the SHA path.
func TestOutputImageDescriptorKeySHA(t *testing.T) {
	img := appwire.OutputImage{SHA: "abc123"}
	if got := outputImageDescriptorKey(img); got != "sha:abc123" {
		t.Fatalf("key = %q, want sha:abc123", got)
	}
}

// TestOutputImageDescriptorKeyURL covers the URL path.
func TestOutputImageDescriptorKeyURL(t *testing.T) {
	img := appwire.OutputImage{URL: "https://example.com/img.png"}
	if got := outputImageDescriptorKey(img); got != "https://example.com/img.png" {
		t.Fatalf("key = %q, want URL", got)
	}
}

// TestOutputImageDescriptorKeyPath covers the Path path.
func TestOutputImageDescriptorKeyPath(t *testing.T) {
	img := appwire.OutputImage{Path: "/tmp/img.png"}
	if got := outputImageDescriptorKey(img); got != "path:/tmp/img.png" {
		t.Fatalf("key = %q, want path:/tmp/img.png", got)
	}
}

// TestOutputImageDescriptorKeyEmpty covers the empty path.
func TestOutputImageDescriptorKeyEmpty(t *testing.T) {
	img := appwire.OutputImage{}
	if got := outputImageDescriptorKey(img); got != "" {
		t.Fatalf("key = %q, want empty", got)
	}
}

// TestOutputImageDescriptorKeyPriority covers that SHA takes priority over URL.
func TestOutputImageDescriptorKeyPriority(t *testing.T) {
	img := appwire.OutputImage{SHA: "sha", URL: "url", Path: "path"}
	if got := outputImageDescriptorKey(img); got != "sha:sha" {
		t.Fatalf("key = %q, want sha:sha (SHA has priority)", got)
	}
}
