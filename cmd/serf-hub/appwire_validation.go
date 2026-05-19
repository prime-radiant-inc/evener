package main

import (
	"fmt"

	"primeradiant.com/serf/internal/appwire"
)

func validateAppWireInputItems(items []appwire.InputItem) error {
	imageCount := 0
	for i, it := range items {
		if it.Type != "image" {
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
