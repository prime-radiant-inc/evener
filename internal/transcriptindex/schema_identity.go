package transcriptindex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/transcript"
)

// schemaID identifies the JSON field shape DecodeEntry's strict decoder
// enforces for transcript.Entry (schema.Turn, llm.Message, and everything
// they embed): every reachable field's json name, path, Go type, and full
// json tag, sorted and hashed. currentProjection folds it into what a build
// records, so a sidecar whose covered entries were validated by a binary
// with a different schema.Turn/llm.Message shape fails closed into a
// rebuild instead of window.go's readEntry — which re-decodes raw
// transcript bytes with the lenient DecodeValidatedEntry — silently
// dropping a field the reading binary doesn't declare (see the "Known gap
// in the phase 1 index" spec section, cross-version schema skew).
//
// A hash beats a hand-maintained version constant here: the bug this guards
// against is exactly a shape change nobody remembered to record, and
// formatVersion/projectionID already show how easy that is to forget.
var schemaID = schemaFieldFingerprint()

// currentProjection is what this build's meta.Projection must equal:
// projectionID with schemaID folded in, so a build whose reachable JSON
// shape differs writes (and requires) a different string. Folding it into
// the field readMeta always compared, rather than a separate field only a
// schema-aware reader would look at, means a binary that predates this
// check entirely still rejects a schema-mismatched sidecar: it runs its own
// plain "does Projection equal my projectionID" comparison exactly as
// before, and a schema-aware build's longer string never matches that
// binary's bare projectionID, so it fails closed into a rebuild too (the
// downgrade direction, not just an old sidecar read by a new binary).
func currentProjection() string {
	return projectionID + ":" + schemaID
}

// schemaFieldFingerprint hashes schemaFields(reflect.TypeFor[transcript.Entry]()).
// A hash beats a hand-maintained version constant here: the bug this guards
// against is exactly a shape change nobody remembered to record, and
// formatVersion/projectionID already show how easy that is to forget.
func schemaFieldFingerprint() string {
	sum := sha256.Sum256([]byte(strings.Join(schemaFields(reflect.TypeFor[transcript.Entry]()), "\n")))
	// The full digest, not a truncated prefix: this hash's whole job is
	// telling two shapes apart, so a shorter one only buys back a few bytes
	// of meta.json at the cost of a real (if small) collision chance.
	return hex.EncodeToString(sum[:])
}

// schemaFields walks every field reachable from t (following pointers,
// slices, arrays, and maps) and returns one descriptor string per reachable
// field, sorted, covering everything a shape change could affect: its json
// name, path, full json struct tag (an option change alone, like "count" ->
// "count,string", decodes a value differently, the same as a renamed
// field), whether it is anonymous (an embedded field promotes its own
// fields to its parent's level, while an explicit named field of the
// identical type nests them under its own key instead — a real decoding
// difference a bare name/type/tag tuple can't tell apart, since both write
// the same name), and the field's Go type together with its underlying
// kind (Type.String() alone conflates two named types sharing a name but
// differing in underlying kind, such as `type ID string` and a
// hypothetical later `type ID int`). Unexported fields are excluded unless
// they are embedded (anonymous) struct types, whose exported fields
// encoding/json promotes and recurses into regardless of the embedding
// field's own visibility; fields tagged json:"-" (the whole tag, not just
// its name half — json:"-," is a field literally named "-", not an omitted
// one) are excluded too. Both match what encoding/json (and so
// DisallowUnknownFields) actually sees. time.Time and interface/`any`
// values are treated as leaves: their own fields are Go runtime state, not
// part of the JSON contract, and an `any`-typed destination has no fixed
// field set for DisallowUnknownFields to enforce in the first place.
func schemaFields(t reflect.Type) []string {
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
				tag := f.Tag.Get("json")
				if tag == "-" {
					// encoding/json omits a field only when its whole tag is
					// exactly "-"; "-," is not omitted, it names the key "-"
					// (the ordinary name-cutting below handles that case:
					// strings.Cut("-,", ",") gives the name "-").
					continue
				}
				name := f.Name
				if n, _, _ := strings.Cut(tag, ","); n != "" {
					name = n
				}
				path := prefix + "." + name
				fields = append(fields, fmt.Sprintf("%s:%s:%s:%s:anon=%v", path, f.Type.String(), f.Type.Kind(), tag, f.Anonymous))
				walk(path, f.Type, depth+1)
			}
		default:
			fields = append(fields, fmt.Sprintf("%s:%s:%s", prefix, t.String(), t.Kind()))
		}
	}
	walk("", t, 0)
	sort.Strings(fields)
	return fields
}
