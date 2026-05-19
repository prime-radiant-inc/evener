package main

import (
	"fmt"

	"primeradiant.com/serf/internal/appwire"
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
		if imageCount > sendMaxImageItems {
			return fmt.Errorf("items exceeds %d-image limit", sendMaxImageItems)
		}
		if len(it.Data) > sendMaxImageBytes {
			return fmt.Errorf("items[%d] %q exceeds %d-byte limit", i, it.Name, sendMaxImageBytes)
		}
	}
	return nil
}

func isImageInputItem(it appwire.InputItem) bool {
	return it.Type == "image" || it.Type == "input_image"
}
