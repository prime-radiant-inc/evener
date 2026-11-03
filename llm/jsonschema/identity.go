// Derived from schema.go in the pinned upstream source. Modified by Evener:
// compact exact numeric identity, framed hashes and shared duplicate traversal.
// See UPSTREAM.md for provenance and the retained Apache-2.0 license.
package jsonschema

import (
	"encoding/binary"
	"hash/maphash"
	"sort"
)

// equals compares ordered arrays, object key sets and exact numeric identities.
// jsonType preserves the existing examined-value admission boundary.
func equals(v1, v2 any) bool {
	v1Type := jsonType(v1)
	if v1Type != jsonType(v2) {
		return false
	}
	switch v1Type {
	case "array":
		arr1, arr2 := v1.([]any), v2.([]any)
		if len(arr1) != len(arr2) {
			return false
		}
		for i := range arr1 {
			if !equals(arr1[i], arr2[i]) {
				return false
			}
		}
		return true
	case "object":
		obj1, obj2 := v1.(map[string]any), v2.(map[string]any)
		if len(obj1) != len(obj2) {
			return false
		}
		for k, value1 := range obj1 {
			value2, ok := obj2[k]
			if !ok || !equals(value1, value2) {
				return false
			}
		}
		return true
	case "number":
		num1, _ := numberValue(v1) // jsonType already checked numeric validity
		num2, _ := numberValue(v2)
		return num1.Equal(num2)
	default:
		return v1 == v2
	}
}

// hash frames the same identity as equals. Hashes select buckets only, never
// establish equality. All numbers use the one admitted converter and compact
// canonical encoding, including signs and normalized zero/exponent spellings.
func hash(v any, h *maphash.Hash) {
	switch jsonType(v) {
	case "null":
		h.WriteByte(0)
	case "boolean":
		h.WriteByte(1)
		if v.(bool) {
			h.WriteByte(1)
		} else {
			h.WriteByte(0)
		}
	case "number":
		h.WriteByte(2)
		num, _ := numberValue(v) // jsonType already checked numeric validity
		hashString(num.String(), h)
	case "string":
		h.WriteByte(3)
		hashString(v.(string), h)
	case "array":
		h.WriteByte(4)
		items := v.([]any)
		hashLength(len(items), h)
		for _, item := range items {
			hash(item, h)
		}
	case "object":
		h.WriteByte(5)
		obj := v.(map[string]any)
		hashLength(len(obj), h)
		props := make([]string, 0, len(obj))
		for prop := range obj {
			props = append(props, prop)
		}
		sort.Strings(props)
		for _, prop := range props {
			hashString(prop, h)
			hash(obj[prop], h)
		}
	}
}

func hashLength(length int, h *maphash.Hash) {
	var frame [8]byte
	binary.LittleEndian.PutUint64(frame[:], uint64(length))
	h.Write(frame[:])
}

func hashString(text string, h *maphash.Hash) {
	hashLength(len(text), h)
	h.WriteString(text)
}

// duplicateItems retains the upstream pairwise path through 20 items and the
// bucket path above 20. Both scan second indices, then earlier candidates, in
// increasing order, stopping at the earliest actual duplicate, not a collision.
func duplicateItems(items []any, hashValue func(any) uint64) (first, second int) {
	if len(items) <= 20 {
		for i := 1; i < len(items); i++ {
			for j := 0; j < i; j++ {
				if equals(items[i], items[j]) {
					return j, i
				}
			}
		}
	} else {
		buckets := make(map[uint64][]int)
		for i, item := range items {
			key := hashValue(item)
			for _, j := range buckets[key] {
				if equals(items[j], item) {
					return j, i
				}
			}
			buckets[key] = append(buckets[key], i)
		}
	}
	return -1, -1
}
