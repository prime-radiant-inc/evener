package jobstore

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/fuzz/aliascheck"
)

func TestJobRecordCloneSharesNoMutableState(t *testing.T) {
	for depth := 2; depth <= 5; depth++ {
		var rec JobRecord
		aliascheck.Populate(reflect.ValueOf(&rec).Elem(), depth)

		clone := rec.Clone()

		if !reflect.DeepEqual(&rec, clone) {
			t.Fatalf("depth %d: clone differs from its original", depth)
		}
		if shared := aliascheck.FindSharedStorage(reflect.ValueOf(&rec), reflect.ValueOf(clone), "JobRecord"); shared != "" {
			t.Fatalf("depth %d: %s", depth, shared)
		}
	}
}

func TestJobRecordCloneOfNilIsNil(t *testing.T) {
	var rec *JobRecord
	if rec.Clone() != nil {
		t.Fatal("nil record cloned to non-nil")
	}
}
