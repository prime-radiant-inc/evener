package agent

import "testing"

func TestTranscriptRef_RoundTripLocal(t *testing.T) {
	ref := encodeRef("", "01JABC")
	if ref != "local:01JABC" {
		t.Fatalf("got %q", ref)
	}
	bucket, id, err := decodeRef(ref)
	if err != nil || bucket != "" || id != "01JABC" {
		t.Fatalf("bucket=%q id=%q err=%v", bucket, id, err)
	}
}

func TestTranscriptRef_RoundTripCrossBucket(t *testing.T) {
	ref := encodeRef("a1b2c3d4e5f60718", "01JABC")
	if ref != "proj:a1b2c3d4e5f60718:01JABC" {
		t.Fatalf("got %q", ref)
	}
	bucket, id, err := decodeRef(ref)
	if err != nil || bucket != "a1b2c3d4e5f60718" || id != "01JABC" {
		t.Fatalf("ref=%q bucket=%q id=%q err=%v", ref, bucket, id, err)
	}
}

func TestTranscriptRef_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "01JABC", "local:", "local:../etc", "proj:onlyonepart", "weird:01J"} {
		if _, _, err := decodeRef(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
