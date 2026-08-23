package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

func TestContentFingerprintEmpty(t *testing.T) {
	fp := contentFingerprint(nil)
	// Non-zero even for empty (the initial fingerprint is zero, so any real
	// fingerprint differs)
	if fp == 0 {
		t.Fatal("fingerprint of empty slice should be non-zero")
	}
}

func TestContentFingerprintConsistent(t *testing.T) {
	entries := []PastEntry{
		{ID: "s1", Meta: schema.SessionMeta{ID: "s1", Name: "Session 1", UpdatedAt: time.Unix(1000, 0)}},
		{ID: "s2", Meta: schema.SessionMeta{ID: "s2", Name: "Session 2", UpdatedAt: time.Unix(2000, 0)}},
	}
	fp1 := contentFingerprint(entries)
	fp2 := contentFingerprint(entries)
	if fp1 != fp2 {
		t.Fatal("same entries should produce same fingerprint")
	}
}

func TestContentFingerprintDifferentEntries(t *testing.T) {
	entries1 := []PastEntry{
		{ID: "s1", Meta: schema.SessionMeta{ID: "s1", Name: "Session 1", UpdatedAt: time.Unix(1000, 0)}},
	}
	entries2 := []PastEntry{
		{ID: "s2", Meta: schema.SessionMeta{ID: "s2", Name: "Session 2", UpdatedAt: time.Unix(2000, 0)}},
	}
	if contentFingerprint(entries1) == contentFingerprint(entries2) {
		t.Fatal("different entries should produce different fingerprints")
	}
}

func TestContentFingerprintDifferentUpdatedAt(t *testing.T) {
	entries1 := []PastEntry{
		{ID: "s1", Meta: schema.SessionMeta{ID: "s1", Name: "Session 1", UpdatedAt: time.Unix(1000, 0)}},
	}
	entries2 := []PastEntry{
		{ID: "s1", Meta: schema.SessionMeta{ID: "s1", Name: "Session 1", UpdatedAt: time.Unix(2000, 0)}},
	}
	if contentFingerprint(entries1) == contentFingerprint(entries2) {
		t.Fatal("different UpdatedAt should produce different fingerprints")
	}
}

func TestContentFingerprintDifferentName(t *testing.T) {
	entries1 := []PastEntry{
		{ID: "s1", Meta: schema.SessionMeta{ID: "s1", Name: "Name A", UpdatedAt: time.Unix(1000, 0)}},
	}
	entries2 := []PastEntry{
		{ID: "s1", Meta: schema.SessionMeta{ID: "s1", Name: "Name B", UpdatedAt: time.Unix(1000, 0)}},
	}
	if contentFingerprint(entries1) == contentFingerprint(entries2) {
		t.Fatal("different Name should produce different fingerprints")
	}
}
