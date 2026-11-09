package appwire

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateMutationParamsRequiredFieldsPerMethod(t *testing.T) {
	identity := []string{"clientMutationId", "expectedInstanceId"}
	with := func(extra ...string) []string { return append(append([]string{}, identity...), extra...) }
	table := map[string][]string{
		MethodTurnStart:                identity,
		MethodTurnSteer:                identity,
		MethodTurnInterrupt:            identity,
		MethodTurnQueue:                identity,
		MethodTurnDrainAsSteer:         with("expectedQueueRevision"),
		MethodTurnPromoteQueuedAsSteer: with("expectedEntryId"),
		MethodTurnCancelQueued:         with("expectedEntryId"),
		MethodThreadClear:              identity,
		MethodNotesHumanSet:            identity,
		MethodUrlsRemove:               identity,
	}
	payload := func(fields []string, omit string) json.RawMessage {
		obj := map[string]any{}
		for _, f := range fields {
			if f == omit {
				continue
			}
			if f == "expectedQueueRevision" {
				obj[f] = 3
			} else {
				obj[f] = "value"
			}
		}
		raw, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for method, fields := range table {
		if err := ValidateMutationParams(method, payload(fields, "")); err != nil {
			t.Errorf("%s with every field: %v", method, err)
		}
		for _, omitted := range fields {
			err := ValidateMutationParams(method, payload(fields, omitted))
			if omitted == "expectedQueueRevision" {
				if err == nil {
					t.Errorf("%s without %s accepted", method, omitted)
				}
				continue
			}
			missing, ok := errors.AsType[MissingMutationFieldError](err)
			if !ok || missing.Field != omitted {
				t.Errorf("%s without %s: got %v", method, omitted, err)
			}
		}
	}
}

func TestValidateMutationParamsIgnoresUnlistedMethods(t *testing.T) {
	if err := ValidateMutationParams(MethodThreadList, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unlisted method: %v", err)
	}
}
