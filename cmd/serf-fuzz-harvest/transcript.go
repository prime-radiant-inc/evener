package main

import (
	"encoding/json"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// harvestToolArgs walks transcript files and emits each model-generated tool
// call's arguments as a (nameIndex, argsBytes) seed for FuzzToolArgsValidate.
// The name is mapped to the exact index in coreNames (the target's table order);
// a call whose tool has no registered match is dropped — it cannot address the
// target's table.
func harvestToolArgs(r *runner, san *Sanitizer, paths []string, coreNames []string) {
	st := r.stat("toolargs")
	index := map[string]int{}
	for i, n := range coreNames {
		index[n] = i
	}

	for _, path := range paths {
		_ = forEachJSONLine(path, func(line []byte) { //nolint:errcheck
			var probe struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(line, &probe) != nil || probe.Kind != "entry" {
				return
			}
			var entry transcript.Entry
			if json.Unmarshal(line, &entry) != nil {
				return
			}
			for _, part := range entry.Turn.Message.Content {
				if part.Kind != llm.ContentToolCall || part.ToolCall == nil {
					continue
				}
				idx, ok := index[part.ToolCall.Name]
				if !ok {
					continue // tool not in the target's table; cannot address it
				}
				st.scanned++
				args := part.ToolCall.Arguments
				if len(args) == 0 {
					args = []byte("{}")
				}
				out, ok := r.scrub(st, san, args, false)
				if !ok {
					continue
				}
				status, err := r.emit.EmitIntBytes(r.dir(dirToolArgsValidate), idx, out)
				if err != nil {
					r.logf("emit error toolargs: %v", err)
					continue
				}
				r.recordEmit(st, status)
			}
		})
	}
}
