package main

// Tests for the field-union registration (appwire.FieldUnions): a union type
// that appears as a struct field renders as the union over its arms, each arm
// is emitted as its own interface, and the union type itself gets no merged
// interface. Crash-fencing spec 08c §9's BoundaryEntry[] is the first such
// field.

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestEmitCatalogSpellsFieldUnionsAsUnions pins the whole rendering: the arms
// exist as interfaces with their own fields, the union type itself has no
// interface, and OperationRecord's field spells the union (parenthesized inside
// the array).
func TestEmitCatalogSpellsFieldUnionsAsUnions(t *testing.T) {
	out := EmitCatalog()
	for _, arm := range []string{
		"BoundaryEntryLocalLinux", "BoundaryEntryLocalDarwin", "BoundaryEntryLocalMarkerless",
		"BoundaryEntryRemoteFencing", "BoundaryEntryUnavailable",
	} {
		body := interfaceBody(t, out, arm)
		if !strings.Contains(body, "\n  kind: string;") {
			t.Errorf("%s carries no kind discriminator: %s", arm, body)
		}
	}
	if strings.Contains(out, "export interface BoundaryEntry {") {
		t.Error("the union type itself was emitted as a merged interface")
	}
	body := interfaceBody(t, out, "OperationRecord")
	want := "\n  orphanBoundary?: (BoundaryEntryLocalLinux | BoundaryEntryLocalDarwin | BoundaryEntryLocalMarkerless | BoundaryEntryRemoteFencing | BoundaryEntryUnavailable)[];"
	if !strings.Contains(body, want) {
		t.Fatalf("OperationRecord.orphanBoundary is not the union over the arms:\n%s", body)
	}
	if !strings.Contains(body, "\n  orphanResolved?: boolean;") {
		t.Fatalf("OperationRecord.orphanResolved is not optional boolean:\n%s", body)
	}
	if !strings.Contains(body, "\n  attestation?: HostOrphanResolveAttestation;") {
		t.Fatalf("OperationRecord.attestation is not the attestation type:\n%s", body)
	}
	// The union's arms are emitted, so every arm's ownership fields exist too.
	remote := interfaceBody(t, out, "BoundaryEntryRemoteFencing")
	for _, field := range []string{"fencingEpoch: FencingEpoch;", "guardEpoch: number;", "leaseEntries: BoundaryLeaseEntry[];"} {
		if !strings.Contains(remote, "\n  "+field) {
			t.Fatalf("BoundaryEntryRemoteFencing carries no %q:\n%s", field, remote)
		}
	}
	lease := interfaceBody(t, out, "BoundaryLeaseEntry")
	if !strings.Contains(lease, "\n  ownership: BoundaryLeaseOwnership;") {
		t.Fatalf("BoundaryLeaseEntry carries no ownership:\n%s", lease)
	}
}

// TestFieldUnionRegistrationMatchesTheWireType pins the registration itself
// against the wire type, so a renamed arm or a dropped variant fails here
// rather than silently rendering a narrower union.
func TestFieldUnionRegistrationMatchesTheWireType(t *testing.T) {
	var found bool
	for _, union := range appwire.FieldUnions {
		if _, ok := union.Union.(appwire.BoundaryEntry); !ok {
			continue
		}
		found = true
		if len(union.Arms) != 5 {
			t.Fatalf("BoundaryEntry registers %d arms, want 5", len(union.Arms))
		}
		for _, arm := range union.Arms {
			if strings.TrimSpace(reflect.TypeOf(arm).Name()) == "" {
				t.Fatalf("an arm carries no name: %T", arm)
			}
		}
	}
	if !found {
		t.Fatal("FieldUnions carries no BoundaryEntry registration")
	}
}
