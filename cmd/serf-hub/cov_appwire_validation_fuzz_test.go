package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func FuzzCovAppWireValidation(f *testing.F) {
	f.Add(uint8(0), 0, "text", "")
	f.Add(uint8(1), hubcore.SendMaxImageItems, "image", "x")
	f.Add(uint8(2), 0, "input_image", strings.Repeat("x", hubcore.SendMaxImageBytes+1))
	f.Fuzz(func(t *testing.T, mode uint8, existing int, typ, data string) {
		if existing < 0 || existing > hubcore.SendMaxImageItems+1 {
			existing = int(mode) % (hubcore.SendMaxImageItems + 2)
		}
		items := []appwire.InputItem{{Type: typ, Name: "seed", Data: []byte(data)}}
		err := validateAppWireInputItemsWithExisting(items, existing)
		if err == nil && isImageInputItem(items[0]) && (existing+1 > hubcore.SendMaxImageItems || len(data) > hubcore.SendMaxImageBytes) {
			t.Fatal("oversized image input accepted")
		}
		_ = validateAppWireInputItems(items)
	})
}
