// Package bucketref owns the shared session-ref dialect and the project-bucket
// helpers both the agent and the doctor consume, so the two components no
// longer re-implement ref formatting and bucket enumeration independently.
//
// The dialect is the single owner of the selector grammar the transcript
// read tools and the doctor accept. The dialect has three spellings, all
// carrying a bare session id as their final token:
//
//   - local:<sid>      — the current / override / scratch bucket
//   - proj:<project-id>:<sid> — a named sibling project bucket
//   - bare <sid>       — search buckets (equivalent to local:<sid> for parsing:
//     both yield projectID="" and the same sid)
//
// A proj: ref cuts at the LAST colon: session ids never contain a colon (pinned
// by agent/doctor.TestSessionIDGrammarCarriesNoColon), so the final colon is
// always the sid boundary and a project id may itself contain colons.
//
// There is deliberately one canonical grammar with two parsers:
//
//   - agent/doctor.parseSelector is the canonical full grammar. Its project-id
//     token admits colons (projectTokenOK rejects only the path-component
//     escapes: empty, dot components, path separators, NUL), matching the sweep
//     (every directory under evener/projects is a bucket, whatever its name).
//   - agent.decodeRef is a deliberately stricter, model-facing subset: its
//     validIDToken rejects colons, dots, and spaces on top of the separators,
//     so a ref the model emits for a non-canonical bucket name is rejected at
//     the agent's read boundary. This is a safety narrowing of the grammar,
//     NOT a grammar change: the doctor's canonical parser still accepts every
//     traversal-safe name, and refs the doctor emits for grammar-incompatible
//     names are suppressed at emission (RefFor returns "").
//
// The constant prefixes below are the single source of truth for the "local:"
// and "proj:" scheme strings; both parsers reference them so the wording lives
// in one place and a future reviewer cannot re-litigate the cut point by
// editing one copy.
package bucketref

// LocalScheme is the "local:" prefix naming the current / override / scratch
// bucket in a session ref. A "local:<sid>" ref parses identically to a bare
// <sid> (both resolve to projectID="" and the same sid); see above.
const LocalScheme = "local:"

// ProjScheme is the "proj:" prefix naming a sibling project bucket in a session
// ref. A "proj:<project-id>:<sid>" ref cuts at the LAST colon: session ids
// never contain a colon (pinned by TestSessionIDGrammarCarriesNoColon), so the
// final colon is always the sid boundary and a project id may itself contain
// colons; see above.
const ProjScheme = "proj:"
