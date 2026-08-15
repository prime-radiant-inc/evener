package delegatestore

import (
	"fmt"
	"os"
)

type ReadDiagnostics struct {
	TornTail bool
}

func ReadEvents(path string) ([]Event, error) {
	events, _, err := ReadEventsWithDiagnostics(path)
	return events, err
}

func ReadEventsWithDiagnostics(path string) ([]Event, ReadDiagnostics, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ReadDiagnostics{}, nil
		}
		return nil, ReadDiagnostics{}, fmt.Errorf("delegatestore: read %s: %w", path, err)
	}
	diagnostics := ReadDiagnostics{TornTail: len(raw) > 0 && raw[len(raw)-1] != '\n'}
	events, err := decodeLog(raw, true)
	if err != nil {
		return nil, diagnostics, err
	}
	if _, err := Fold(events); err != nil {
		return nil, diagnostics, fmt.Errorf("delegatestore: fold: %w", err)
	}
	return cloneEvents(events), diagnostics, nil
}
