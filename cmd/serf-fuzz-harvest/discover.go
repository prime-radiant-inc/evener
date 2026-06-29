package main

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
)

// recordedSources are the on-disk files a single state directory yields, grouped
// by the fuzz surface they feed. Each list is sorted for deterministic harvest.
type recordedSources struct {
	sse         []string // api-raw.jsonl
	transcripts []string // *.transcript.jsonl
	appwire     []string // appwire-frames.jsonl
	http        []string // hub-http.jsonl
	jobs        []string // sessions/<SID>/jobs.jsonl
}

// discoverSources walks a state directory and buckets the recorded files by
// surface. A recursive walk handles every on-disk layout (the XDG
// serf/projects/<hash> buckets and the override/scratch root alike) without
// re-deriving bucket paths.
func discoverSources(stateDir string) (recordedSources, error) {
	var s recordedSources
	err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable subtree: skip it, don't abort the whole walk
		}
		if d.IsDir() {
			return nil
		}
		switch base := d.Name(); {
		case base == "api-raw.jsonl":
			s.sse = append(s.sse, path)
		case base == "appwire-frames.jsonl":
			s.appwire = append(s.appwire, path)
		case base == "hub-http.jsonl":
			s.http = append(s.http, path)
		case base == "jobs.jsonl":
			s.jobs = append(s.jobs, path)
		case strings.HasSuffix(base, ".transcript.jsonl"):
			s.transcripts = append(s.transcripts, path)
		}
		return nil
	})
	if err != nil {
		return recordedSources{}, err
	}
	sort.Strings(s.sse)
	sort.Strings(s.transcripts)
	sort.Strings(s.appwire)
	sort.Strings(s.http)
	sort.Strings(s.jobs)
	return s, nil
}

// isPersonalStateDir reports whether dir is the developer's own default state
// root (~/.serf with no SERF_STATE_DIR override). Such a source must always be
// shape-scrubbed — --keep-values is ignored for it (decision 6).
func isPersonalStateDir(dir string) bool {
	if envvars.SERFStateDir.Getenv() != "" {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	def, err := filepath.Abs(cmdutil.DefaultStateRoot())
	if err != nil {
		return false
	}
	return abs == def
}

// Target directories (relative to --out-root) the harvester writes into. Each is
// a fuzz target's native testdata/fuzz/<FuzzName>/ so Go auto-loads the seeds.
const (
	dirParseSSE           = "llm/testdata/fuzz/FuzzParseSSE"
	dirOpenAIResponses    = "llm/providers/openai/testdata/fuzz/FuzzOpenAIResponsesMetamorphic"
	dirOpenAIChatComplete = "llm/providers/openai/testdata/fuzz/FuzzOpenAIChatCompletionsMetamorphic"
	dirAnthropicStream    = "llm/providers/anthropic/testdata/fuzz/FuzzAnthropicStreamMetamorphic"
	dirGeminiStream       = "llm/providers/google/testdata/fuzz/FuzzGeminiStreamMetamorphic"
	dirOpenAICompatStream = "llm/providers/openaicompat/testdata/fuzz/FuzzOpenAICompatStreamMetamorphic"
	dirToolArgsValidate   = "agent/testdata/fuzz/FuzzToolArgsValidate"
	dirMessageDecode      = "appwire/testdata/fuzz/FuzzMessageDecode"
	dirMethodParams       = "appwire/testdata/fuzz/FuzzMethodParams"
	dirWebHandler         = "cmd/serf-hub/testdata/fuzz/FuzzWebHandler"
	dirJobstoreEvent      = "testdata/fuzz-jobs-staging/JobstoreEventDecode"   // staging until 8.1 names its target
	dirJobstoreSequence   = "testdata/fuzz-jobs-staging/JobstoreEventSequence" // staging until 8.1 names its Fold target
)
