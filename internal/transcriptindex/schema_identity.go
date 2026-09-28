package transcriptindex

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/transcript"
)

// schemaID identifies the JSON field shape DecodeEntry's strict decoder
// enforces for transcript.Entry (schema.Turn, llm.Message, and everything
// they embed): every reachable field's json name, path, and Go type, sorted
// and hashed. readMeta folds it into what a build records, so a sidecar whose
// covered entries were validated by a binary with a different
// schema.Turn/llm.Message shape fails closed into a rebuild instead of
// window.go's readEntry — which re-decodes raw transcript bytes with the
// lenient DecodeValidatedEntry — silently dropping a field the reading
// binary doesn't declare (see the "Known gap in the phase 1 index" spec
// section, cross-version schema skew).
//
// A hash beats a hand-maintained version constant here: the bug this guards
// against is exactly a shape change nobody remembered to record, and
// formatVersion/projectionID already show how easy that is to forget.
var schemaID = schemaFieldFingerprint()

// schemaFieldFingerprint walks every field reachable from transcript.Entry
// (following pointers, slices, arrays, and maps) and returns a short
// deterministic hash of their json names, paths, Go types, and full json
// struct tags (an option change alone, like "count" -> "count,string",
// decodes a value differently, the same as a renamed field). Unexported
// fields are excluded unless they are embedded (anonymous) struct types,
// whose exported fields encoding/json promotes and recurses into regardless
// of the embedding field's own visibility; fields tagged json:"-" are
// excluded too. Both match what encoding/json (and so DisallowUnknownFields)
// actually sees. time.Time and interface/`any` values are treated as leaves:
// their own fields are Go runtime state, not part of the JSON contract, and
// an `any`-typed destination has no fixed field set for
// DisallowUnknownFields to enforce in the first place.
func schemaFieldFingerprint() string {
	var fields []string
	var walk func(prefix string, t reflect.Type, depth int)
	walk = func(prefix string, t reflect.Type, depth int) {
		// A depth guard, not a cycle detector: nothing reachable from Entry
		// today is self-referential, but a future type that becomes so
		// should stop this walk rather than loop forever.
		if depth > 32 {
			return
		}
		switch t.Kind() {
		case reflect.Pointer:
			walk(prefix, t.Elem(), depth+1)
		case reflect.Slice, reflect.Array:
			walk(prefix+"[]", t.Elem(), depth+1)
		case reflect.Map:
			walk(prefix+"[key]", t.Key(), depth+1)
			walk(prefix+"[val]", t.Elem(), depth+1)
		case reflect.Struct:
			if t.PkgPath() == "time" && t.Name() == "Time" {
				fields = append(fields, prefix+":time.Time")
				return
			}
			for f := range t.Fields() {
				if f.PkgPath != "" {
					// Unexported: encoding/json ignores it, unless it is an
					// embedded (anonymous) struct type, whose exported
					// fields it promotes and recurses into regardless of
					// the embedding field's own visibility.
					if !f.Anonymous {
						continue
					}
					et := f.Type
					for et.Kind() == reflect.Pointer {
						et = et.Elem()
					}
					if et.Kind() != reflect.Struct {
						continue
					}
				}
				name := f.Name
				tag, hasTag := f.Tag.Lookup("json")
				if hasTag {
					n, _, _ := strings.Cut(tag, ",")
					if n == "-" {
						continue
					}
					if n != "" {
						name = n
					}
				}
				path := prefix + "." + name
				// The full tag, not just the name: an option change alone
				// (e.g. json:"count" -> json:"count,string") changes how a
				// value decodes just as much as a renamed field does.
				fields = append(fields, path+":"+f.Type.String()+":"+tag)
				walk(path, f.Type, depth+1)
			}
		default:
			fields = append(fields, prefix+":"+t.String())
		}
	}
	walk("", reflect.TypeFor[transcript.Entry](), 0)
	sort.Strings(fields)
	sum := sha256.Sum256([]byte(strings.Join(fields, "\n")))
	return hex.EncodeToString(sum[:8])
}
