package main

import (
	"fmt"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func validateAppWireInputItems(items []appwire.InputItem) error {
	return validateAppWireInputItemsWithExisting(items, 0)
}

func validateAppWireInputItemsWithExisting(items []appwire.InputItem, imageCount int) error {
	for i, it := range items {
		if !isImageInputItem(it) {
			continue
		}
		imageCount++
		if imageCount > hubcore.SendMaxImageItems {
			return fmt.Errorf("items exceeds %d-image limit", hubcore.SendMaxImageItems)
		}
		if len(it.Data) > hubcore.SendMaxImageBytes {
			return fmt.Errorf("items[%d] %q exceeds %d-byte limit", i, it.Name, hubcore.SendMaxImageBytes)
		}
	}
	return nil
}

func isImageInputItem(it appwire.InputItem) bool {
	return it.Type == "image" || it.Type == "input_image"
}
