package mergeq

import "testing"

// FuzzParseGitHubAnswers drives every parser that reads `gh` output with the
// same bytes: the four of them decode the wire contract the queue's decisions
// rest on, and a value that decodes into a usable Status, a run list, a merge
// or a removal event must not also be able to make any of them panic, allocate
// unboundedly, or return a success the caller cannot act on.
//
// The oracle is the contract each parser documents, asserted from the decoded
// value rather than from any rendered sentence:
//
//   - a read that reports a merge always yields a sha to report, never the
//     empty string: the renderer would print "sha=" and a caller would record
//     a merge it cannot name.
//   - the renderings a decoded value feeds -- Status.MergeSHA and
//     RunRef.String -- never come back empty, whatever bytes decoded. They are
//     what an operator reads, and a blank one hides the run or the merge.
//   - every decoded removal reason gets advice: classifyDropReason's zero
//     return would leave the drop line ending in a colon.
//   - no parser panics or returns a success it cannot describe, on any bytes.
//
// Deliberately not asserted: that a decoded queue entry carries a state. The
// query asks for `mergeQueueEntry{position state}`, so GitHub always supplies
// one, and a response that omits it decodes to an entry with an empty state
// that the timeout line prints honestly as `state=`. Rejecting that here would
// be inventing strictness the wire contract does not have.
func FuzzParseGitHubAnswers(f *testing.F) {
	seeds := []string{
		`{"data":{"repository":{"pullRequest":{"id":"PR_1","state":"OPEN","baseRefName":"main","mergeCommit":null,"mergeQueueEntry":{"position":3,"state":"QUEUED"}}}}}`,
		`{"data":{"repository":{"pullRequest":{"id":"PR_1","state":"MERGED","baseRefName":"main","mergeCommit":{"oid":"abc123"}}}}}`,
		`{"data":{"repository":{"pullRequest":null}}}`,
		`{"data":{"repository":{"pullRequest":{"id":"PR_1","state":"OPEN","baseRefName":"null"}}}}`,
		`{"state":"MERGED","mergeCommit":{"oid":"deadbeef"}}`,
		`{"state":"OPEN","mergeCommit":null}`,
		`{"state":"MERGED"}`,
		`[]`,
		`[{"databaseId":42,"url":"https://example.invalid/42","conclusion":"failure","headBranch":"gh-readonly-queue/main/pr-1-deadbeef"}]`,
		`[{"databaseId":null,"headBranch":""}]`,
		`{"createdAt":"2026-11-17T12:00:00Z","reason":"failed_checks"}`,
		`{"createdAt":"not a time","reason":""}`,
		`{"event":"removed_from_merge_queue"}`,
		`null`,
		`{}`,
		``,
		`{"data":{"repository":{"pullRequest":{"mergeQueueEntry":{"position":-1}}}}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if st, err := parseStatus(raw); err == nil {
			if st.MergeSHA() == "" {
				t.Fatalf("MergeSHA came back empty for %q", raw)
			}
		}

		if sha, merged, err := parseMergedCommit(raw); err == nil && merged {
			if sha == "" {
				t.Fatalf("a merge with an empty sha from %q", raw)
			}
		}

		if runs, err := parseRuns(raw); err == nil {
			for i := range runs {
				if (RunRef{Group: &runs[i]}).String() == "" {
					t.Fatalf("a decoded run rendered as nothing from %q", raw)
				}
			}
		}

		if ev, err := parseDropEvent(raw); err == nil && ev != nil {
			_, advice := classifyDropReason(ev.Reason, RunRef{})
			if advice == "" {
				t.Fatalf("no advice for a decoded reason %q from %q", ev.Reason, raw)
			}
		}
	})
}
