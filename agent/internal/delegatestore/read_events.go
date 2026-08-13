package delegatestore

import (
	"fmt"
	"os"
)

func ReadEvents(path string) ([]Event, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	events, err := decodeLog(raw, true)
	if err != nil {
		return nil, err
	}
	if _, err := Fold(events); err != nil {
		return nil, fmt.Errorf("delegatestore: fold: %w", err)
	}
	return cloneEvents(events), nil
}
