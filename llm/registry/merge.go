package registry

import (
	"maps"
	"reflect"
	"sort"
	"strings"
)

// wholesaleFields are Caps fields that replace as a unit on overlay rather
// than merging key-wise (spec §4.1).
var wholesaleFields = map[string]bool{
	"EffortValues": true, "InputModalities": true, "ReasoningControls": true,
	"Cost": true, "FinishReasonMap": true,
}

// keyWiseFields are Caps map fields that merge key by key (spec §4.1).
var keyWiseFields = map[string]bool{"Fields": true, "ChatTemplateKwargs": true}

// mergeCaps overlays src onto dst: a non-nil pointer or non-nil slice in src
// replaces; key-wise maps merge per key; nil inherits. Every field or map key
// that src set is recorded in prov under tag (spec §4.1).
func mergeCaps(dst *Caps, src Caps, tag string, prov map[string]string) {
	dv := reflect.ValueOf(dst).Elem()
	sv := reflect.ValueOf(src)
	for i := 0; i < sv.NumField(); i++ {
		f := sv.Type().Field(i)
		sf := sv.Field(i)
		df := dv.Field(i)
		if sf.IsNil() {
			continue
		}
		switch {
		case keyWiseFields[f.Name]:
			if df.IsNil() {
				df.Set(reflect.MakeMap(df.Type()))
			}
			iter := sf.MapRange()
			for iter.Next() {
				df.SetMapIndex(iter.Key(), iter.Value())
				if prov != nil {
					prov[f.Name+"."+iter.Key().String()] = tag
				}
			}
		case wholesaleFields[f.Name] || sf.Kind() == reflect.Ptr || sf.Kind() == reflect.Slice || sf.Kind() == reflect.Map:
			df.Set(sf)
			if prov != nil {
				prov[f.Name] = tag
			}
		}
	}
}

// mergeTransport overlays the non-empty scalar fields and merges the maps of
// src onto dst (spec §4.1).
func mergeTransport(dst *Transport, src Transport) {
	setIf := func(d *string, s string) {
		if s != "" {
			*d = s
		}
	}
	setIf(&dst.Preset, src.Preset)
	setIf(&dst.Auth, src.Auth)
	setIf(&dst.AuthHeader, src.AuthHeader)
	setIf(&dst.BaseURL, src.BaseURL)
	setIf(&dst.HostRule, src.HostRule)
	setIf(&dst.Endpoint, src.Endpoint)
	setIf(&dst.StreamEndpoint, src.StreamEndpoint)
	setIf(&dst.ModelsEndpoint, src.ModelsEndpoint)
	setIf(&dst.CountTokensEndpoint, src.CountTokensEndpoint)
	dst.Vars = mergeStringMap(dst.Vars, src.Vars)
	dst.VarsEnv = mergeStringMap(dst.VarsEnv, src.VarsEnv)
	if len(src.Body) > 0 {
		if dst.Body == nil {
			dst.Body = map[string]any{}
		}
		maps.Copy(dst.Body, src.Body)
	}
}

// mergeStringMap returns dst with src's keys overlaid; a nil dst is allocated
// only when src has keys.
func mergeStringMap(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]string{}
	}
	maps.Copy(dst, src)
	return dst
}

// isGlob reports whether a models key is a glob row (contains `*`).
func isGlob(key string) bool { return strings.Contains(key, "*") }

// matchGlob matches id against pattern, where `*` matches any run of
// characters (including `/` and `.`); everything else is literal and
// case-sensitive (spec §4.1).
func matchGlob(pattern, id string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == id
	}
	if !strings.HasPrefix(id, parts[0]) {
		return false
	}
	id = id[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(id, parts[i])
		if idx < 0 {
			return false
		}
		id = id[idx+len(parts[i]):]
	}
	return strings.HasSuffix(id, parts[len(parts)-1])
}

// sortGlobs orders glob patterns shorter-first, then lexically, so the more
// specific pattern applies last and wins (spec §4.1).
func sortGlobs(patterns []string) []string {
	out := append([]string(nil), patterns...)
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) < len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}
