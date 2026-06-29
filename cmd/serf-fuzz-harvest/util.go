package main

import (
	"bufio"
	"os"
)

// jsonlMaxLineBytes matches the transcript writer's per-line ceiling so a long
// recorded line (a big SSE stream, a large frame) is never silently truncated.
const jsonlMaxLineBytes = 128 << 20

// forEachJSONLine calls fn with each non-empty line of a JSONL file. The line
// slice is only valid for the duration of the call (it is reused). A file open
// or scan error is returned; fn itself drops malformed lines, so it has no error
// to propagate.
func forEachJSONLine(path string, fn func(line []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), jsonlMaxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		fn(line)
	}
	return sc.Err()
}
