package main

import "bytes"

// harvestJobs walks sessions/<SID>/jobs.jsonl files as raw NDJSON (no jobstore
// import — the package is internal to the agent module) and seeds the jobstore
// FuzzJobEventLogReplay target directly. That target takes a raw jobs.jsonl blob
// and splits it on newlines, so BOTH shapes are valid seeds for it: each scrubbed
// event line (one-Event decode + single-event fold) and each session's full
// scrubbed event sequence (multi-event fold/replay round-trip). The fixed
// scrubber emits valid RFC3339 timestamps, so these seeds DECODE and reach the
// deep fold/round-trip oracles rather than only the decode-rejection floor.
func harvestJobs(r *runner, san *Sanitizer, paths []string) {
	st := r.stat("jobs")
	dir := r.dir(dirJobstoreReplay)
	for _, path := range paths {
		var seq bytes.Buffer
		_ = forEachJSONLine(path, func(line []byte) { //nolint:errcheck
			st.scanned++
			out, ok := r.scrub(st, san, line, false)
			if !ok {
				return
			}
			r.emitBytesTo(st, out, dir)
			seq.Write(out)
			seq.WriteByte('\n')
		})
		if seq.Len() > 0 {
			r.emitBytesTo(st, seq.Bytes(), dir)
		}
	}
}
