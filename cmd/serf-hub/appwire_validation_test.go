package main

import (
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestValidateAppWireInputItemsRejectsOversizedImage(t *testing.T) {
	err := validateAppWireInputItems([]appwire.InputItem{{
		Type: "input_image",
		Name: "too-large.png",
		Data: make([]byte, sendMaxImageBytes+1),
	}})
	if err == nil {
		t.Fatal("validateAppWireInputItems accepted oversized image")
	}
}

func TestValidateAppWireInputItemsCountsExistingImages(t *testing.T) {
	items := []appwire.InputItem{{Type: "input_image", Data: []byte("x")}}
	if err := validateAppWireInputItemsWithExisting(items, sendMaxImageItems); err == nil {
		t.Fatal("validateAppWireInputItemsWithExisting accepted combined image count over limit")
	}
}

func TestValidateAppWireInputItemsRejectsTooManyImages(t *testing.T) {
	items := make([]appwire.InputItem, sendMaxImageItems+1)
	for i := range items {
		items[i] = appwire.InputItem{Type: "image", Data: []byte("x")}
	}
	if err := validateAppWireInputItems(items); err == nil {
		t.Fatal("validateAppWireInputItems accepted too many images")
	}
}

func TestTranscriptJSONLMaxLineCoversMaxImagePayload(t *testing.T) {
	encodedImageBytes := ((sendMaxImageBytes + 2) / 3) * 4
	encodedPayloadBytes := sendMaxImageItems*encodedImageBytes + 1024*1024
	if transcriptJSONLMaxLineBytes < encodedPayloadBytes {
		t.Fatalf("transcriptJSONLMaxLineBytes=%d, want at least %d", transcriptJSONLMaxLineBytes, encodedPayloadBytes)
	}
}

func TestSendMaxRequestBytesCoversMaxImagePayload(t *testing.T) {
	encodedImageBytes := ((sendMaxImageBytes + 2) / 3) * 4
	encodedPayloadBytes := sendMaxImageItems*encodedImageBytes + 1024*1024
	if sendMaxRequestBytes < encodedPayloadBytes {
		t.Fatalf("sendMaxRequestBytes=%d, want at least %d", sendMaxRequestBytes, encodedPayloadBytes)
	}
}
