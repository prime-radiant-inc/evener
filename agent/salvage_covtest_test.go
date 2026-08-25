package agent

import (
	"testing"
)

// TestPartialJSONStringFields_NonStringKey covers the path where the
// key is not a string (line 61-63: rest[0] != '"').
func TestPartialJSONStringFields_NonStringKey(t *testing.T) {
	t.Parallel()
	got := partialJSONStringFields(`{123:"foo"}`)
	if len(got) != 0 {
		t.Fatalf("expected empty for non-string key, got %+v", got)
	}
}

// TestPartialJSONStringFields_MissingColon covers the path where the
// colon is missing after the key (lines 69-71).
func TestPartialJSONStringFields_MissingColon(t *testing.T) {
	t.Parallel()
	got := partialJSONStringFields(`{"key" "value"}`)
	if len(got) != 0 {
		t.Fatalf("expected empty for missing colon, got %+v", got)
	}
}

// TestPartialJSONStringFields_EmptyAfterColon covers the path where
// the value is empty after the colon (lines 73-75).
func TestPartialJSONStringFields_EmptyAfterColon(t *testing.T) {
	t.Parallel()
	got := partialJSONStringFields(`{"key":`)
	if len(got) != 0 {
		t.Fatalf("expected empty for truncated after colon, got %+v", got)
	}
}

// TestPartialJSONStringFields_TruncatedNonStringValue covers the path
// where a non-string value is truncated (lines 84-88: skipPartialJSONValue
// returns !ok).
func TestPartialJSONStringFields_TruncatedNonStringValue(t *testing.T) {
	t.Parallel()
	got := partialJSONStringFields(`{"count":5`)
	if len(got) != 0 {
		t.Fatalf("expected empty for truncated non-string value, got %+v", got)
	}
}

// TestPartialJSONStringFields_EmptyAfterValue covers the path where
// rest is empty after a value (lines 91-93).
func TestPartialJSONStringFields_EmptyAfterValue(t *testing.T) {
	t.Parallel()
	got := partialJSONStringFields(`{"key":"value"`)
	if len(got) != 1 || got[0].Key != "key" || got[0].Value != "value" {
		t.Fatalf("expected one field, got %+v", got)
	}
}

// TestPartialJSONStringFields_NoCommaAfterValue covers the path where
// the character after a value is not a comma (line 97-98: default case).
func TestPartialJSONStringFields_NoCommaAfterValue(t *testing.T) {
	t.Parallel()
	got := partialJSONStringFields(`{"key":"value" "other":"x"}`)
	if len(got) != 1 || got[0].Key != "key" {
		t.Fatalf("expected one field, got %+v", got)
	}
}

// TestSkipPartialJSONValue_TruncatedStringInObject covers the truncated
// string inside a nested object (line 166-167).
func TestSkipPartialJSONValue_TruncatedStringInObject(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`{"x":"unterminated`)
	if ok {
		t.Fatal("expected ok=false for truncated string in object")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}

// TestSkipPartialJSONValue_NestedDepth covers depth++ in a nested
// object (line 171).
func TestSkipPartialJSONValue_NestedDepth(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`{"a":{"b":"c"}}`)
	if !ok {
		t.Fatal("expected ok=true for complete nested object")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}

// TestSkipPartialJSONValue_UnterminatedObject covers the unterminated
// nested object case (line 180).
func TestSkipPartialJSONValue_UnterminatedObject(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`{"a":1`)
	if ok {
		t.Fatal("expected ok=false for unterminated object")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}

// TestSkipPartialJSONValue_UnterminatedArray covers the unterminated
// array case (line 180).
func TestSkipPartialJSONValue_UnterminatedArray(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`[1,2,3`)
	if ok {
		t.Fatal("expected ok=false for unterminated array")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}

// TestSkipPartialJSONValue_TruncatedScalar covers the scalar value that
// runs to end of input (lines 187-189).
func TestSkipPartialJSONValue_TruncatedScalar(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`true`)
	if ok {
		t.Fatal("expected ok=false for truncated scalar")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}

// TestSkipPartialJSONValue_ScalarFollowedByComma covers a scalar
// followed by a delimiter (lines 190).
func TestSkipPartialJSONValue_ScalarFollowedByComma(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`123,`)
	if !ok {
		t.Fatal("expected ok=true for scalar before comma")
	}
	if rest != "," {
		t.Fatalf("expected rest=',', got %q", rest)
	}
}

// TestSkipPartialJSONValue_CompleteArray covers a complete array.
func TestSkipPartialJSONValue_CompleteArray(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`[1,2,3]`)
	if !ok {
		t.Fatal("expected ok=true for complete array")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}

// TestSkipPartialJSONValue_NestedArrayInObject covers nested arrays
// inside objects (line 171 depth++).
func TestSkipPartialJSONValue_NestedArrayInObject(t *testing.T) {
	t.Parallel()
	rest, ok := skipPartialJSONValue(`{"a":[1,[2,3]]}`)
	if !ok {
		t.Fatal("expected ok=true for nested array in object")
	}
	if rest != "" {
		t.Fatalf("expected empty rest, got %q", rest)
	}
}
