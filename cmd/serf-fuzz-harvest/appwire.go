package main

import (
	"encoding/json"

	"primeradiant.com/serf/appwire"
)

// recordedFrame mirrors the WS frame recorder's JSONL line.
type recordedFrame struct {
	Dir   string `json:"dir"`
	Frame string `json:"frame"`
}

// harvestAppwire walks appwire-frames.jsonl files. Each frame seeds
// FuzzMessageDecode (full scrubbed frame); request/notification frames also seed
// FuzzMethodParams as (methodIndex, scrubbedParams) against the Methods catalog.
func harvestAppwire(r *runner, san *Sanitizer, paths []string) {
	st := r.stat("appwire")
	methodIndex := map[string]int{}
	for i, m := range appwire.Methods {
		methodIndex[m.Name] = i
	}

	for _, path := range paths {
		_ = forEachJSONLine(path, func(line []byte) { //nolint:errcheck
			var fr recordedFrame
			if json.Unmarshal(line, &fr) != nil || fr.Frame == "" {
				return
			}
			frame := []byte(fr.Frame)
			st.scanned++

			if out, ok := r.scrub(st, san, frame, false); ok {
				r.emitBytesTo(st, out, r.dir(dirMessageDecode))
			}

			var probe struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(frame, &probe) != nil || probe.Method == "" || len(probe.Params) == 0 {
				return
			}
			idx, ok := methodIndex[probe.Method]
			if !ok {
				return
			}
			out, ok := r.scrub(st, san, probe.Params, false)
			if !ok {
				return
			}
			status, err := r.emit.EmitIntBytes(r.dir(dirMethodParams), idx, out)
			if err != nil {
				r.logf("emit error appwire params: %v", err)
				return
			}
			r.recordEmit(st, status)
		})
	}
}
