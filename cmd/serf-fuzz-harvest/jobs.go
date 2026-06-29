package main

import "bytes"

// harvestJobs walks sessions/<SID>/jobs.jsonl files as raw NDJSON (no jobstore
// import — the package is internal to the agent module). Each line seeds 8.1's
// jobstore-Event decode target; each session's full scrubbed event sequence
// seeds the Fold/replay target. Until 8.1 names those targets the seeds land in
// a staging dir and the counts are reported, so the extractor is exercisable now.
func harvestJobs(r *runner, san *Sanitizer, paths []string) {
	st := r.stat("jobs")
	for _, path := range paths {
		var seq bytes.Buffer
		_ = forEachJSONLine(path, func(line []byte) { //nolint:errcheck
			st.scanned++
			out, ok := r.scrub(st, san, line, false)
			if !ok {
				return
			}
			r.emitBytesTo(st, out, r.dir(dirJobstoreEvent))
			seq.Write(out)
			seq.WriteByte('\n')
		})
		if seq.Len() > 0 {
			r.emitBytesTo(st, seq.Bytes(), r.dir(dirJobstoreSequence))
		}
	}
}
