package appwire

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPendingAskPageDecodeRejectsNullKeys(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"offset":null}`,
		`{"Offset":null}`,
		`{"limit":null}`,
		`{"expectedAskRevision":null}`,
		`{"expectedAskBootGeneration":null}`,
		`{"expectedAskBootGeneration":""}`,
		`{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","offset":null}`,
		`{"ref":"local:root","unexpectedField":true}`,
	} {
		var params ThreadReadParams
		if err := json.Unmarshal([]byte(raw), &params); err == nil {
			t.Errorf("read decoded an invalid page or unknown field: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"ref":"local:root"}`,
		`{"ref":"local:root","subscribe":true,"includeTurns":true}`,
		`{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","offset":0,"limit":0}`,
	} {
		var params ThreadReadParams
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			t.Errorf("read rejected a valid page or ordinary read: %v", err)
		}
	}
}

func TestPendingAskPageReadValidation(t *testing.T) {
	t.Parallel()
	zero := uint64(0)
	n := 0
	valid := ThreadReadParams{Ref: "local:root", ExpectedAskRevision: &zero, ExpectedAskBootGeneration: "1", Offset: &n}
	for _, tc := range []struct {
		name    string
		params  ThreadReadParams
		invalid bool
	}{
		{"ordinary", ThreadReadParams{}, false},
		{"zero revision", valid, false},
		{"missing pair", ThreadReadParams{Ref: "local:root", Limit: &n}, true},
		{"missing ref", ThreadReadParams{ExpectedAskRevision: &zero, ExpectedAskBootGeneration: "1"}, true},
		{"partial pair", ThreadReadParams{Ref: "local:root", ExpectedAskRevision: &zero}, true},
		{"blank boot", ThreadReadParams{Ref: "local:root", ExpectedAskRevision: &zero, ExpectedAskBootGeneration: " "}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateThreadReadParams(tc.params)
			if !tc.invalid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var wire WireError
			if !errors.As(err, &wire) || wire.Code != CodeInvalidParams {
				t.Fatalf("invalid page accepted or wrong error: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*ThreadReadParams)
	}{
		{"subscribe", func(p *ThreadReadParams) { p.Subscribe = true }},
		{"replace", func(p *ThreadReadParams) { p.ReplaceSubscription = true }},
		{"turns", func(p *ThreadReadParams) { p.IncludeTurns = true }},
		{"window", func(p *ThreadReadParams) { p.TurnWindow = &ThreadTurnWindow{Latest: 1} }},
		{"held", func(p *ThreadReadParams) { p.HeldSnapshot = &SnapshotIdentity{} }},
		{"continuation", func(p *ThreadReadParams) {
			p.AnswerContinuation = &AnswerContinuationQuery{AncestorTurnID: "a", CandidateTurnID: "b"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			if err := ValidateThreadReadParams(p); err == nil {
				t.Fatal("page accepted incompatible history mode")
			}
		})
	}
	p := valid
	p.LiveOnly = true
	if err := ValidateThreadReadParams(p); err != nil {
		t.Fatalf("live-only page rejected: %v", err)
	}
}
