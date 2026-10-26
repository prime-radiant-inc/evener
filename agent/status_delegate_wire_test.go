package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire/appwiretest"
)

// Every field the agent reports on a delegate reaches the wire row.
func TestDelegateStatusInfoAppwireInfoCarriesEveryField(t *testing.T) {
	var in DelegateStatusInfo
	appwiretest.FillNonZero(&in)
	got := reflect.ValueOf(in.AppwireInfo())
	for f := range reflect.TypeOf(in).Fields() {
		field := got.FieldByName(f.Name)
		if !field.IsValid() {
			t.Fatalf("EvenerDelegateInfo has no field %s for the agent status field", f.Name)
		}
		if field.IsZero() {
			t.Errorf("AppwireInfo dropped %s", f.Name)
		}
	}
}

// The wire row owns its pointers and slices: mutating it leaves the source
// status untouched.
func TestDelegateStatusInfoAppwireInfoDoesNotAliasSource(t *testing.T) {
	var in DelegateStatusInfo
	appwiretest.FillNonZero(&in)
	var want DelegateStatusInfo
	appwiretest.FillNonZero(&want)

	out := in.AppwireInfo()
	*out.RunningForMS++
	*out.StructuredValid = !*out.StructuredValid
	out.Usage.InputTokens++
	out.Worktree.Branch += "x"
	out.Warnings[0] += "x"
	out.Diagnostics[0] += "x"
	out.Message[0]++
	out.StructuredResult[0]++

	if !reflect.DeepEqual(in, want) {
		t.Fatalf("mutating the wire row changed the source status:\n got %+v\nwant %+v", in, want)
	}
}

func TestDelegateStatusInfoAppwireInfoKeepsNilPointersNil(t *testing.T) {
	out := DelegateStatusInfo{DelegateID: "dlg"}.AppwireInfo()
	if out.DelegateID != "dlg" || out.Usage != nil || out.Worktree != nil || out.RunningForMS != nil {
		t.Fatalf("zero status mapped to %+v", out)
	}
}
