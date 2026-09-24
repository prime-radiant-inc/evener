package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Finding is the atomic output of a doctor audit, per
// internal/bundled/skills/doctoring-evener/references/finding-contract.md.
// The JSON field names below (including evidence's and suggestedFix's) are
// the contract's exact spelling — camelCase, not this package's usual
// snake_case — because the contract is a cross-tool wire format, not a
// doctor-CLI-internal shape.
type Finding struct {
	Signature    string          `json:"signature"`
	Severity     string          `json:"severity"`
	Category     string          `json:"category"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Evidence     FindingEvidence `json:"evidence"`
	SuggestedFix SuggestedFix    `json:"suggestedFix"` //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
}

// FindingEvidence is the contract's evidence object. At least one sub-field
// is populated per Finding; doctorCommand is either a runnable command or
// empty. When every affected session is non-reproducible (bare sid
// ambiguous across unsafe buckets), doctorCommand is empty — not runnable,
// and the non-reproducibility disclosure lives in Description prose (round 9
// finding 2: the previous "# not reproducible:" comment form is gone because
// cmd.exe does not treat # as a comment).
type FindingEvidence struct {
	SessionRefs []string `json:"sessionRefs,omitempty"` //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
	// TotalSessionRefs is the true distinct-session count when SessionRefs
	// was structurally capped at evidenceSessionRefCap, or when the ref
	// list was shorter than the true count (deduped bare sids across
	// non-canonical buckets — round 7 finding 2); 0 means not capped and
	// no dedup discrepancy.
	TotalSessionRefs int      `json:"totalSessionRefs,omitempty"` //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
	WatchIDs         []string `json:"watchIds,omitempty"`         //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
	DeliveryIDs      []string `json:"deliveryIds,omitempty"`      //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
	TranscriptTurns  []int    `json:"transcriptTurns,omitempty"`  //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
	DoctorCommand    string   `json:"doctorCommand"`              //nolint:tagliatelle // doctor Finding wire contract is camelCase — always present per finding-contract.md (round 10 finding 2: empty when all sessions non-reproducible, not omitted)
	LogSnippets      []string `json:"logSnippets,omitempty"`      //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
}

// SuggestedFix is the contract's routing directive: diagnosis (report-only),
// runbook (extend), or skill (heal, gated).
type SuggestedFix struct {
	Type       string `json:"type"`
	FileHint   string `json:"fileHint,omitempty"`   //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
	SymbolHint string `json:"symbolHint,omitempty"` //nolint:tagliatelle // doctor Finding wire contract is camelCase (finding-contract.md)
}

var validSeverities = map[string]bool{"low": true, "medium": true, "high": true}
var validSuggestedFixTypes = map[string]bool{"diagnosis": true, "runbook": true, "skill": true}
var validAuditOps = map[string]bool{">=": true, ">": true, "<=": true, "<": true, "==": true, "!=": true}

// auditCondition is one metric/op/value clause. A check with more than one
// condition (the "all" form) ANDs them together — the mechanism the
// `longest_identical_run.errors && length >= 3` shape in the plan needs,
// since a single metric/op/value triple cannot express a compound predicate.
type auditCondition struct {
	Metric string `yaml:"metric"`
	Op     string `yaml:"op"`
	Value  any    `yaml:"value"`
}

// auditCheckRaw is the YAML shape of one `audit:` block entry, before
// normalization collapses the single-condition and "all" forms into one
// Conditions list.
type auditCheckRaw struct {
	Title        string           `yaml:"title"`
	Severity     string           `yaml:"severity"`
	Category     string           `yaml:"category"`
	SuggestedFix string           `yaml:"suggested_fix"`
	Metric       string           `yaml:"metric"`
	Op           string           `yaml:"op"`
	Value        any              `yaml:"value"`
	All          []auditCondition `yaml:"all"`
}

// AuditCheck is one runbook-defined mechanical threshold, normalized: either
// its single metric/op/value collapses to a one-element Conditions, or its
// "all" list carries several conditions ANDed together. A session "trips" a
// check when every condition holds.
type AuditCheck struct {
	Title        string
	Severity     string
	Category     string
	SuggestedFix string
	Conditions   []auditCondition
}

// Runbook is a parsed runbook: its mechanical `audit:` checks plus every
// CLASSIFY prose bullet the audit: block didn't capture. Prose steps are
// for an LLM operator's judgment, not the mechanical driver — but the
// driver never drops them silently; it reports them as manual.
type Runbook struct {
	Name        string
	Checks      []AuditCheck
	ManualSteps []string
}

// apiHealthMetricNames names the apilog.* metric paths metricSource.resolve
// answers from an APIHealthResult rather than an APILogTotals summary --
// needsAPIHealth keys on exactly this set so a runbook using only the older
// apilog.calls/.empties/.errors/.avg_latency_ms totals never pays for the
// separate --health decode pass.
var apiHealthMetricNames = map[string]bool{
	"apilog.recorded_empty":     true,
	"apilog.retry_storm_groups": true,
	"apilog.unsettled_groups":   true,
}

// needsAPILog reports whether any check references one of APILogTotals'
// apilog.* metrics (calls/empties/errors/avg_latency_ms), so RunAudit only
// pays for that summary decode when a check actually needs it.
func (rb Runbook) needsAPILog() bool {
	for _, c := range rb.Checks {
		for _, cond := range c.Conditions {
			if strings.HasPrefix(cond.Metric, "apilog.") && !apiHealthMetricNames[cond.Metric] && !strings.HasPrefix(cond.Metric, "apilog.errors_by_class.") {
				return true
			}
		}
	}
	return false
}

// needsAPIHealth reports whether any check references one of APIHealthResult's
// apilog.* metrics (recorded_empty/retry_storm_groups/unsettled_groups/
// errors_by_class.<class>), so RunAudit only pays for the --health decode
// pass when a check actually needs it.
func (rb Runbook) needsAPIHealth() bool {
	for _, c := range rb.Checks {
		for _, cond := range c.Conditions {
			if apiHealthMetricNames[cond.Metric] || strings.HasPrefix(cond.Metric, "apilog.errors_by_class.") {
				return true
			}
		}
	}
	return false
}

// addCheck appends check to rb.Checks, rejecting a duplicate (category,
// title) pair. auditSignature keys on exactly that pair, so two checks
// sharing it would silently collapse into one Finding at audit time: the
// second check's tripped sessions would merge into the first's evidence,
// freezing whichever check's severity/description happened to be recorded
// first and misattributing evidence between two different conditions. This
// is caught at parse time instead, loudly, naming both colliding checks.
func (rb *Runbook) addCheck(check AuditCheck) error {
	for _, existing := range rb.Checks {
		if existing.Category == check.Category && existing.Title == check.Title {
			return fmt.Errorf("duplicate audit check: title %q category %q is defined twice (severities %q and %q) — two checks sharing category+title would collapse into one Finding, since that pair is exactly what auditSignature keys on; give each check a distinct title",
				check.Title, check.Category, existing.Severity, check.Severity)
		}
	}
	rb.Checks = append(rb.Checks, check)
	return nil
}

// ParseRunbook parses a runbook markdown document: every fenced code block
// that YAML-decodes to a non-empty top-level `audit:` list becomes a set of
// AuditChecks (see writing-runbooks.md for the block schema) — and it must
// be inside CLASSIFY, or ParseRunbook fails loudly rather than silently
// accepting a misplaced block — and every top-level `- ` bullet in the
// CLASSIFY section outside any fence — the prose an LLM operator must
// judge, not a mechanical check — becomes a ManualStep. A bullet's wrapped
// continuation lines (indented follow-on text, up to the next bullet,
// blank line, heading, or fence) are joined onto that same step with a
// space rather than dropped, so a bullet's sentence never gets silently
// truncated at its first physical line. A runbook with neither a check nor
// a step is not audit-executable, so that's a loud error rather than a
// silent no-op.
func ParseRunbook(name string, content []byte) (Runbook, error) {
	rb := Runbook{Name: name}
	inFence := false
	var fenceLines []string
	inClassify := false
	// openStep indexes the ManualStep currently accepting indented
	// continuation lines, or -1 when no bullet is open (just started, or
	// closed by a blank line/heading/fence/new bullet).
	openStep := -1

	for line := range strings.SplitSeq(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				checks, found := parseAuditFence(strings.Join(fenceLines, "\n"))
				if found {
					if !inClassify {
						return Runbook{}, fmt.Errorf("runbook %s: found an audit: block (first check %q) outside the CLASSIFY section — audit: blocks must live inside CLASSIFY", name, checks[0].Title)
					}
					for _, raw := range checks {
						check, err := normalizeCheck(raw)
						if err != nil {
							return Runbook{}, fmt.Errorf("runbook %s: invalid audit check %q: %w", name, raw.Title, err)
						}
						if err := rb.addCheck(check); err != nil {
							return Runbook{}, fmt.Errorf("runbook %s: %w", name, err)
						}
					}
				}
				fenceLines = nil
			}
			inFence = !inFence
			openStep = -1
			continue
		}
		if inFence {
			fenceLines = append(fenceLines, line)
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			inClassify = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, "##")), "CLASSIFY")
			openStep = -1
			continue
		}
		if !inClassify {
			continue
		}
		switch {
		case trimmed == "":
			openStep = -1
		case strings.HasPrefix(trimmed, "- "):
			rb.ManualSteps = append(rb.ManualSteps, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
			openStep = len(rb.ManualSteps) - 1
		case line != trimmed && openStep >= 0:
			// An indented, non-bullet line while a bullet is open is that
			// bullet's wrapped continuation.
			rb.ManualSteps[openStep] += " " + trimmed
		default:
			openStep = -1
		}
	}

	if len(rb.Checks) == 0 && len(rb.ManualSteps) == 0 {
		return Runbook{}, fmt.Errorf("runbook %s: no audit: block and no CLASSIFY prose steps found", name)
	}
	return rb, nil
}

// ParseRunbookFromFS resolves a runbook by name from a runbooks/ dir inside
// fsys (path: <skill>/runbooks/<name>.md) and parses it. The two runbook
// consumers — the evener-doctor CLI and the doctor_evener tool — share this
// resolution so their name→runbook mapping cannot drift; each supplies its
// own FS (the bundled skills embed for both, tests substitute fixtures).
// Name sanitization rejects path traversal before the FS join.
func ParseRunbookFromFS(fsys fs.FS, skill, name string) (Runbook, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return Runbook{}, fmt.Errorf("invalid runbook name %q", name)
	}
	content, err := fs.ReadFile(fsys, path.Join(skill, "runbooks", name+".md"))
	if err != nil {
		return Runbook{}, fmt.Errorf("load runbook %q: %w", name, err)
	}
	return ParseRunbook(name, content)
}

// parseAuditFence tries to YAML-decode a fenced block as an `audit:` list,
// reporting whether it found one. A fence that isn't one (e.g. the INSPECT
// section's evener-doctor invocation) either fails to decode into the wrapper
// shape or decodes with an empty list; both read as found=false, not an
// error — ParseRunbook decides separately whether a *found* block sits in
// the wrong section.
func parseAuditFence(content string) (checks []auditCheckRaw, found bool) {
	var wrapper struct {
		Audit []auditCheckRaw `yaml:"audit"`
	}
	if err := yaml.Unmarshal([]byte(content), &wrapper); err != nil {
		return nil, false
	}
	if len(wrapper.Audit) == 0 {
		return nil, false
	}
	return wrapper.Audit, true
}

// normalizeCheck validates a raw audit: entry and collapses its
// single-condition or "all" form into Conditions. Validation is strict and
// fails loud: a malformed runbook is a defect in the doctor's own
// machinery, and it must never silently degrade to "no findings".
func normalizeCheck(raw auditCheckRaw) (AuditCheck, error) {
	if raw.Title == "" {
		return AuditCheck{}, errors.New("missing title")
	}
	if !validSeverities[raw.Severity] {
		return AuditCheck{}, fmt.Errorf("severity %q must be one of low, medium, high", raw.Severity)
	}
	if raw.Category == "" {
		return AuditCheck{}, errors.New("missing category (see finding-contract.md)")
	}
	suggestedFix := raw.SuggestedFix
	if suggestedFix == "" {
		suggestedFix = "diagnosis"
	}
	if !validSuggestedFixTypes[suggestedFix] {
		return AuditCheck{}, fmt.Errorf("suggested_fix %q must be one of diagnosis, runbook, skill", suggestedFix)
	}

	conditions := raw.All
	if len(conditions) == 0 {
		conditions = []auditCondition{{Metric: raw.Metric, Op: raw.Op, Value: raw.Value}}
	} else if raw.Metric != "" || raw.Op != "" || raw.Value != nil {
		return AuditCheck{}, errors.New("both a top-level metric/op/value and an \"all\" list were given; use one or the other")
	}
	for _, cond := range conditions {
		if cond.Metric == "" {
			return AuditCheck{}, errors.New("condition missing metric")
		}
		if !validAuditOps[cond.Op] {
			return AuditCheck{}, fmt.Errorf("condition %q: op %q must be one of >=, >, <=, <, ==, !=", cond.Metric, cond.Op)
		}
	}

	return AuditCheck{
		Title:        raw.Title,
		Severity:     raw.Severity,
		Category:     raw.Category,
		SuggestedFix: suggestedFix,
		Conditions:   conditions,
	}, nil
}

// evaluate reports whether every one of the check's conditions holds against
// source — the AND semantics the "all" form and the single-condition form
// share.
func (c AuditCheck) evaluate(source metricSource) (bool, error) {
	for _, cond := range c.Conditions {
		actual, err := source.resolve(cond.Metric)
		if err != nil {
			return false, err
		}
		ok, err := compareMetric(actual, cond.Op, cond.Value)
		if err != nil {
			return false, fmt.Errorf("metric %s: %w", cond.Metric, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// metricSource is one session's resolved metric values: Task 2's health
// result always, plus apilog totals (Runbook.needsAPILog) and/or apilog
// health (Runbook.needsAPIHealth) when a check needs them — each decode is
// skipped otherwise.
type metricSource struct {
	health        HealthResult
	apilog        APILogTotals
	haveAPILog    bool
	apiHealth     APIHealthResult
	haveAPIHealth bool
}

// resolve maps a runbook's dotted metric path to its current value, per the
// convention documented in writing-runbooks.md: `jobs.<reason>` and
// `jobs.zero_output_terminal` read JobsHealth; `longest_identical_run.length`
// / `.errors` (alias `.all_errors`) / `.tool` read IdenticalRun;
// `truncation_warnings`, `stale_notifications`, `user_corrections` are
// top-level HealthResult ints; `steering.<kind>`, `tool_calls.<tool>`, and
// `tool_errors.<tool>.<class>` read the corresponding maps; `apilog.calls` /
// `.empties` / `.errors` / `.avg_latency_ms` read APILogTotals; `apilog.
// recorded_empty` / `.retry_storm_groups` / `.unsettled_groups` /
// `.errors_by_class.<class>` read APIHealthResult. An absent map key reads
// as zero (a metric that never occurred is legitimately zero, not an error)
// — only an unknown namespace or malformed path is a loud error.
func (m metricSource) resolve(path string) (any, error) {
	namespace, rest, _ := strings.Cut(path, ".")
	switch namespace {
	case "jobs":
		switch {
		case rest == "":
			return nil, fmt.Errorf("metric %q: expected jobs.<reason> or jobs.zero_output_terminal", path)
		case rest == "zero_output_terminal":
			return m.health.Jobs.ZeroOutputTerminal, nil
		case strings.HasPrefix(rest, "zero_output_terminal."):
			return nil, fmt.Errorf("metric %q: jobs.zero_output_terminal takes no further path segments", path)
		default:
			return m.health.Jobs.ByTerminalReason[rest], nil
		}
	case "longest_identical_run":
		switch rest {
		case "length":
			return m.health.LongestIdenticalRun.Length, nil
		case "errors", "all_errors":
			return m.health.LongestIdenticalRun.AllErrors, nil
		case "tool":
			return m.health.LongestIdenticalRun.Tool, nil
		default:
			return nil, fmt.Errorf("unknown metric %q", path)
		}
	case "truncation_warnings":
		if rest != "" {
			return nil, fmt.Errorf("metric %q: truncation_warnings takes no further path segments", path)
		}
		return m.health.TruncationWarnings, nil
	case "stale_notifications":
		if rest != "" {
			return nil, fmt.Errorf("metric %q: stale_notifications takes no further path segments", path)
		}
		return m.health.StaleNotifications, nil
	case "user_corrections":
		if rest != "" {
			return nil, fmt.Errorf("metric %q: user_corrections takes no further path segments", path)
		}
		return m.health.UserCorrections, nil
	case "steering":
		if rest == "" {
			return nil, fmt.Errorf("metric %q: expected steering.<kind>", path)
		}
		return m.health.Steering[rest], nil
	case "tool_calls":
		if rest == "" {
			return nil, fmt.Errorf("metric %q: expected tool_calls.<tool>", path)
		}
		return m.health.ToolCalls[rest], nil
	case "tool_errors":
		tool, class, ok := strings.Cut(rest, ".")
		if !ok || tool == "" || class == "" {
			return nil, fmt.Errorf("metric %q: expected tool_errors.<tool>.<class>", path)
		}
		return m.health.ToolErrors[tool][class], nil
	case "apilog":
		if apiHealthMetricNames[path] || strings.HasPrefix(rest, "errors_by_class.") {
			if !m.haveAPIHealth {
				return nil, fmt.Errorf("metric %q: apilog health was not loaded for this check", path)
			}
			switch {
			case rest == "recorded_empty":
				return m.apiHealth.RecordedEmpty, nil
			case rest == "retry_storm_groups":
				return m.apiHealth.RetryStormGroups, nil
			case rest == "unsettled_groups":
				return m.apiHealth.UnsettledGroups, nil
			case strings.HasPrefix(rest, "errors_by_class."):
				class := strings.TrimPrefix(rest, "errors_by_class.")
				if class == "" {
					return nil, fmt.Errorf("metric %q: expected apilog.errors_by_class.<class>", path)
				}
				return m.apiHealth.ErrorsByClass[class], nil
			default:
				return nil, fmt.Errorf("unknown metric %q", path)
			}
		}
		if !m.haveAPILog {
			return nil, fmt.Errorf("metric %q: apilog totals were not loaded for this check", path)
		}
		switch rest {
		case "calls":
			return m.apilog.Calls, nil
		case "empties":
			return m.apilog.Empties, nil
		case "errors":
			return m.apilog.Errors, nil
		case "avg_latency_ms":
			return int(m.apilog.AvgLatencyMs), nil
		default:
			return nil, fmt.Errorf("unknown metric %q", path)
		}
	default:
		return nil, fmt.Errorf("unknown metric namespace %q in %q", namespace, path)
	}
}

// compareMetric applies op to actual (an int or bool resolved from a
// session's metrics) against want (a scalar decoded from the runbook's
// YAML). Type mismatches — a boolean op against a numeric metric or
// vice versa — are a runbook authoring error, reported loudly rather than
// coerced.
func compareMetric(actual any, op string, want any) (bool, error) {
	switch a := actual.(type) {
	case bool:
		w, ok := want.(bool)
		if !ok {
			return false, fmt.Errorf("metric is boolean but value %v (%T) is not", want, want)
		}
		switch op {
		case "==":
			return a == w, nil
		case "!=":
			return a != w, nil
		default:
			return false, fmt.Errorf("operator %q is not valid for a boolean metric (use == or !=)", op)
		}
	case string:
		w, ok := want.(string)
		if !ok {
			return false, fmt.Errorf("metric is a string but value %v (%T) is not", want, want)
		}
		switch op {
		case "==":
			return a == w, nil
		case "!=":
			return a != w, nil
		default:
			return false, fmt.Errorf("operator %q is not valid for a string metric (use == or !=)", op)
		}
	case int:
		af := float64(a)
		wf, err := toFloat(want)
		if err != nil {
			return false, err
		}
		return compareFloat(af, op, wf)
	default:
		return false, fmt.Errorf("unsupported metric value type %T", actual)
	}
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case float64:
		return n, nil
	default:
		return 0, fmt.Errorf("expected a number, got %v (%T)", v, v)
	}
}

func compareFloat(a float64, op string, b float64) (bool, error) {
	switch op {
	case ">=":
		return a >= b, nil
	case ">":
		return a > b, nil
	case "<=":
		return a <= b, nil
	case "<":
		return a < b, nil
	case "==":
		return a == b, nil
	case "!=":
		return a != b, nil
	default:
		return false, fmt.Errorf("unsupported operator %q", op)
	}
}

// AuditOpts selects the session set RunAudit checks: exactly one of an
// explicit selector list or a --since window (scanning every bucket, like
// ListSessions).
type AuditOpts struct {
	Sessions []string
	Since    time.Duration
}

// AuditSummaryRow is one line of the pattern × session-count summary table:
// one row per deduped Finding.
type AuditSummaryRow struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Sessions int    `json:"sessions"`
}

// AuditResult is one evener-doctor audit run: the runbook's mechanical checks
// evaluated over the resolved session set and deduped by Finding signature,
// the summary table, every manual (prose/LLM-judgment) step the runbook
// named, and every session RunAudit could not read — never silently
// dropped, mirroring SessionsResult.Unreadable.
type AuditResult struct {
	Runbook         string              `json:"runbook"`
	SessionsChecked int                 `json:"sessions_checked"`
	Findings        []Finding           `json:"findings"`
	Summary         []AuditSummaryRow   `json:"summary"`
	Manual          []string            `json:"manual"`
	Unreadable      []UnreadableSession `json:"unreadable"`
}

// nonReproSession carries the bucket context for a non-reproducible session
// — one whose bare sid is ambiguous across shell-unsafe buckets so
// DoctorCommand cannot reproduce it via --sessions. The disclosure comment
// names each session with its bucket so distinct sessions sharing a SID
// across different unsafe buckets are distinguishable (finding 2).
type nonReproSession struct {
	bucket string
	sid    string
}

// formatNonReproSessions formats non-reproducible sessions for Description
// prose, each named with bucket context so distinct sessions sharing a SID
// across different unsafe buckets are distinguishable (finding 2). Sorted
// by bucket then sid for deterministic output. The budget parameter is the
// remaining slot count in the shared evidenceSessionRefCap budget after
// reproducible refs are accounted for (round 10 finding 3: Description has
// one 200-entry budget across all session references, not independent caps).
// Returns the formatted disclosure and the number of non-reproducible
// sessions omitted past the remaining budget.
func formatNonReproSessions(sessions map[string]nonReproSession, budget int) (desc string, omitted int) {
	keys := make([]string, 0, len(sessions))
	for k := range sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	capped := keys
	if len(capped) > budget {
		omitted = len(capped) - budget
		capped = capped[:budget]
	}
	parts := make([]string, 0, len(capped))
	for _, k := range capped {
		s := sessions[k]
		parts = append(parts, fmt.Sprintf("%s in bucket %q", s.sid, s.bucket))
	}
	if len(parts) == 0 {
		return "", omitted
	}
	desc = strings.Join(parts, ", ") + " (bucket name shell-unsafe, bare id ambiguous across buckets)"
	return desc, omitted
}

// followSelector returns the emission selector for DoctorCommand — the
// selector that re-addresses one session in a shell reproduction line:
// refFor's transcript ref (proj:<id>:<sid> or local:<sid>) when refFor
// produced one, a bucket-qualified proj:<id>:<sid> selector when refFor
// did not but the bucket name is safe for the comma-joined --sessions
// reproduction line (safeTokenForRepro), or the bare session id as a last
// resort. refFor gates its proj: emission on the canonical
// identifier.ValidateProjectID alphabet ([A-Za-z0-9-]). followSelector's
// middle branch widens that to every name that round-trips the CLI's
// comma-joined --sessions grammar and is inert in a shell line — covering
// shell-safe-but-non-canonical legacy names like hex-style bucket
// directories (e.g. 0123456789abcdef) that ValidateProjectID rejects.
// Names that fail even the reproduction-safety check — commas, whitespace,
// shell metacharacters, path separators, NUL — fall back to the bare
// session id, preserving pre-FU2 behavior for them.
// Round 9 finding 1: followSelector is the EMISSION selector (DoctorCommand
// only). Internal reads (the --since sweep, TranscriptHealth, APILog,
// APIHealth) use readSelector, which gates on projectTokenOK (wider than
// safeTokenForRepro) because no shell is involved — so colliding sessions
// in shell-unsafe buckets resolve via the bucket-qualified form instead of
// landing as Unreadable.
func followSelector(projectID, sessionID string) string {
	if ref := refFor(projectID, sessionID); ref != "" {
		return ref
	}
	if safeTokenForRepro(projectID) {
		return projRef(projectID, sessionID)
	}
	return sessionID
}

// readSelector returns the bucket-qualified selector for internal reads
// (Locate, TranscriptHealth, APILog, APIHealth) — the widest form that
// resolves: proj:<project-id>:<sid> when the bucket name is
// traversal-safe (projectTokenOK, which admits every directory name
// globBuckets enumerates, including shell-unsafe names like "dollar$bucket"
// or "has space"), or the bare session id as a last resort. This is wider
// than followSelector (which gates proj: emission on safeTokenForRepro for
// shell safety in DoctorCommand) because internal reads involve no shell —
// the selector only needs to resolve via Locate, not round-trip a command
// line. Round 9 finding 1: the --since sweep previously used followSelector,
// so colliding unsafe-bucket sessions (names passing projectTokenOK but
// failing safeTokenForRepro) fell back to the bare sid, which is ambiguous
// across buckets, landing both as Unreadable. readSelector emits the
// bucket-qualified form so Locate resolves each session unambiguously.
func readSelector(projectID, sessionID string) string {
	if projectID == "" {
		return sessionID
	}
	if projectTokenOK(projectID) {
		return projRef(projectID, sessionID)
	}
	return sessionID
}

// safeTokenForRepro reports whether a bucket name is safe to emit as a
// proj:<name>:<sid> token in DoctorCommand's comma-joined --sessions
// reproduction line. It is the emission-site constraint, deliberately
// wider than the canonical identifier.ValidateProjectID grammar: hex-style
// legacy bucket names (e.g. 0123456789abcdef) pass this check but fail
// ValidateProjectID. The predicate rejects: empty, the dot components,
// path separators, NUL (path-escape defense, same as projectTokenOK);
// commas (the CLI splits --sessions on ','); whitespace (shell
// word-break); shell metacharacters that alter command behavior
// including glob expansion ($, backtick, ;, |, &, (, ), <, >, !, #, ~, ",
// ', {, }, =, *, ?, [, ]); and cmd.exe metacharacters: % (variable
// expansion %VAR%) and ^ (escape character — round 10 finding 1).
func safeTokenForRepro(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00, \t\r\n$`;|&()<>=!#~\"'{}*?[]%^")
}

// RunAudit resolves opts' session set, runs runbook's mechanical checks
// against each session's Task 2 health metrics (and apilog totals, only
// decoded when a check needs them), and dedups tripped checks into Findings
// by signature: one Finding per (runbook, category, title) tripped this run,
// with every affected session listed in its evidence. A session RunAudit
// cannot read is recorded in Unreadable, never silently skipped — matching
// ListSessions' sweep for the --since path, and Locate's own error for an
// explicit --sessions selector that doesn't resolve.
func RunAudit(stateBase string, runbook Runbook, opts AuditOpts) (AuditResult, error) {
	if len(opts.Sessions) > 0 && opts.Since > 0 {
		return AuditResult{}, errors.New("--sessions and --since are mutually exclusive")
	}
	if len(opts.Sessions) == 0 && opts.Since <= 0 {
		return AuditResult{}, errors.New("either --sessions or --since must be given")
	}

	res := AuditResult{Runbook: runbook.Name, Manual: append([]string(nil), runbook.ManualSteps...)}

	var refs []string
	if len(opts.Sessions) > 0 {
		refs = opts.Sessions
	} else {
		sweep, err := ListSessions(stateBase, SessionsOpts{Since: opts.Since})
		if err != nil {
			return AuditResult{}, err
		}
		for _, s := range sweep.Sessions {
			refs = append(refs, readSelector(s.Bucket, s.SessionID))
		}
		res.Unreadable = append(res.Unreadable, sweep.Unreadable...)
	}

	needsAPILog := runbook.needsAPILog()
	needsAPIHealth := runbook.needsAPIHealth()
	now := time.Now()
	findingsBySignature := map[string]*Finding{}
	checkBySignature := map[string]AuditCheck{}
	var signatureOrder []string
	// doctorRefsBySig tracks the doctor-CLI-safe reproducible selectors per
	// finding signature for DoctorCommand's --sessions reproduction line.
	// These are followSelector outputs that resolve via Locate (not
	// ambiguous across buckets). Separate from SessionRefs (finding 3):
	// SessionRefs carries the honest session identifier (canonical proj: ref
	// or bare sid for non-canonical buckets) for a doctor-CLI/human handle on
	// this tree, while DoctorCommand carries doctor-CLI-safe selectors
	// (proj:<safe-name>:<sid> via safeTokenForRepro) for the doctor CLI's
	// --sessions grammar. Cross-bucket agent resolution of bare sids from
	// non-canonical buckets arrives when #2205 (fu-transcript-lookup) lands.
	doctorRefsBySig := map[string][]string{}
	// nonReproBySig tracks non-reproducible sessions per finding signature
	// with bucket context, so DoctorCommand's disclosure names each
	// individually (finding 2): distinct sessions sharing a SID across
	// different unsafe buckets must each be disclosed, not collapsed into
	// one bare-id entry. Keyed by projectID+"\x00"+sessionID for dedup.
	nonReproBySig := map[string]map[string]nonReproSession{}
	// sessionsBySig tracks the true count of distinct sessions per finding
	// signature (keyed by projectID+"\x00"+sessionID), so the summary and
	// Description reflect every audited session even when SessionRefs
	// carries a deduped bare id (finding 2: two sessions sharing a SID
	// across different buckets each count).
	sessionsBySig := map[string]map[string]bool{}

	for _, ref := range refs {
		paths, err := Locate(stateBase, ref)
		if err != nil {
			res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: ref, TranscriptRef: ref, Error: err.Error()})
			continue
		}
		// sel is the selector every read uses (TranscriptHealth, APILog,
		// APIHealth — all call Locate internally). For explicit --sessions
		// inputs, ref is the user-supplied selector that already resolved via
		// Locate, so it is safe for re-resolution. For the --since sweep, ref
		// is readSelector output (proj:<name>:<sid> or bare sid) — the widest
		// form that resolves, including shell-unsafe bucket names (round 9
		// finding 1: the sweep previously used followSelector, which falls back
		// to the bare sid for shell-unsafe names, making colliding sessions
		// ambiguous and Unreadable; readSelector emits the bucket-qualified
		// form so Locate resolves each unambiguously).
		sel := ref
		// agentSel is the honest session identifier for SessionRefs:
		// refFor's transcript ref (proj:<canonical>:<sid> or local:<sid>)
		// when refFor produced one, or the bare session id as a last resort
		// (finding 3: agent-side read_transcript validates the project token
		// with ValidateProjectID, so proj:<non-canonical>:<sid> refs that
		// followSelector's safeTokenForRepro branch emits are rejected —
		// SessionRefs carries the bare sid instead, the honest session
		// identifier and a doctor-CLI/human handle on this tree. Cross-bucket
		// agent resolution of bare sids from non-canonical buckets arrives
		// when #2205 (fu-transcript-lookup) removes the ValidateProjectID
		// filter from enumerateBuckets).
		agentSel := refFor(paths.ProjectID, paths.SessionID)
		if agentSel == "" {
			agentSel = paths.SessionID
		}
		// doctorSel is the doctor-CLI-safe selector for DoctorCommand:
		// followSelector's output (canonical proj: ref, safeTokenForRepro
		// proj: ref, or bare sid). This may diverge from agentSel for
		// non-canonical shell-safe bucket names (e.g. hex names): doctorSel
		// carries proj:<hex>:<sid> (the doctor CLI round-trips it fine),
		// while agentSel carries the bare sid (agent tools reject the proj:
		// form). For canonical names and for unsafe names, both are the same.
		doctorSel := followSelector(paths.ProjectID, paths.SessionID)
		// When followSelector falls through to the bare session id (bucket
		// name fails both ValidateProjectID and safeTokenForRepro), check
		// whether the bare id resolves uniquely. If it is ambiguous across
		// buckets, it cannot reproduce the session in DoctorCommand — track
		// it as non-reproducible so DoctorCommand omits it and discloses
		// the non-reproducibility in the Description prose (round 9 finding 2).
		// This triggers for the explicit --sessions path with shell-unsafe
		// bucket names; the --since sweep now resolves those via readSelector
		// (round 9 finding 1), but the emission selector (followSelector) still
		// falls back to the bare sid for DoctorCommand.
		nonReproducible := false
		if doctorSel == paths.SessionID {
			if _, err := Locate(stateBase, doctorSel); err != nil {
				nonReproducible = true
			}
		}
		// Round 12 finding 1: a bare sid in SessionRefs (agentSel) is
		// colliding when it also exists in a canonical bucket. The agent-side
		// resolver (enumerateBuckets) filters by ValidateProjectID, so it
		// skips non-canonical buckets — a bare sid from a hex bucket that
		// also exists in a canonical bucket resolves silently to the wrong
		// (canonical) session. The doctor-side Locate (globBuckets sees all
		// dirs) reports the ambiguity. When agentSel is a bare sid and it is
		// ambiguous via Locate, check whether any OTHER bucket containing the
		// sid has a canonical name (refFor returns a non-empty proj: ref).
		// If so, the agent would resolve to that canonical bucket silently —
		// omit the colliding bare sid from SessionRefs. The Description's
		// reproducible portion already carries the bucket-qualified ref
		// (proj:<bucket>:<sid> via doctorSel) so the session is traceable.
		// This does not fire when the sid is ambiguous across non-canonical
		// buckets only (hex + hex) — the agent can't resolve any of them, so
		// the bare sid is still the honest identifier. It does not affect
		// the nonReproducible classification, unique bare sids, or canonical
		// proj: refs.
		collidingBareSid := false
		if agentSel == paths.SessionID {
			if _, err := Locate(stateBase, agentSel); err != nil {
				// Bare sid is ambiguous or not found. Check whether any other
				// bucket containing the sid has a canonical name (refFor
				// returns non-empty) — if so, the agent resolves to it silently.
				buckets, _, _ := resolveBuckets(stateBase)
				for _, b := range buckets {
					if b.projectID == paths.ProjectID {
						continue // same bucket
					}
					if sessionInBucket(b, paths.SessionID) && refFor(b.projectID, paths.SessionID) != "" {
						collidingBareSid = true
						break
					}
				}
			}
		}
		health, err := TranscriptHealth(stateBase, sel)
		if err != nil {
			res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: paths.SessionID, TranscriptRef: paths.TranscriptRef, Error: err.Error()})
			continue
		}
		source := metricSource{health: health}
		if needsAPILog {
			apiRes, err := APILog(stateBase, sel, APILogOpts{SummaryOnly: true})
			if err != nil {
				res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: paths.SessionID, TranscriptRef: paths.TranscriptRef, Error: err.Error()})
				continue
			}
			source.apilog = apiRes.Totals
			source.haveAPILog = true
		}
		if needsAPIHealth {
			apiHealthRes, err := APIHealth(stateBase, sel)
			if err != nil {
				res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: paths.SessionID, TranscriptRef: paths.TranscriptRef, Error: err.Error()})
				continue
			}
			source.apiHealth = apiHealthRes
			source.haveAPIHealth = true
		}
		res.SessionsChecked++

		for _, check := range runbook.Checks {
			tripped, err := check.evaluate(source)
			if err != nil {
				return AuditResult{}, fmt.Errorf("runbook %s check %q on session %s: %w", runbook.Name, check.Title, sel, err)
			}
			if !tripped {
				continue
			}
			sig := auditSignature(runbook.Name, check.Category, check.Title, now)
			f, ok := findingsBySignature[sig]
			if !ok {
				f = &Finding{
					Signature:    sig,
					Severity:     check.Severity,
					Category:     check.Category,
					Title:        check.Title,
					SuggestedFix: SuggestedFix{Type: check.SuggestedFix},
				}
				findingsBySignature[sig] = f
				checkBySignature[sig] = check
				signatureOrder = append(signatureOrder, sig)
			}
			// Track the true distinct session count per signature (finding 2):
			// keyed by (projectID, sessionID) so two sessions sharing a SID
			// across different buckets each count, even when SessionRefs
			// carries a deduped bare id.
			sessionKey := paths.ProjectID + "\x00" + paths.SessionID
			if sessionsBySig[sig] == nil {
				sessionsBySig[sig] = map[string]bool{}
			}
			sessionsBySig[sig][sessionKey] = true
			// SessionRefs carries the honest session identifier (finding 3):
			// refFor's transcript ref for canonical names, or the bare
			// session id for non-canonical names (agent-side read_transcript
			// rejects proj:<non-canonical>:<sid> via ValidateProjectID). The
			// bare id is the honest session handle and a doctor-CLI/human
			// identifier on this tree (cross-bucket agent resolution arrives
			// with #2205). It is deduped when distinct sessions share a SID —
			// the true count is in sessionsBySig and the Description, not in
			// len(SessionRefs).
			// SessionRefs carries the honest session identifier — except when
			// the bare sid is colliding (round 12 finding 1): a bare sid from
			// a non-canonical bucket that also exists in a canonical bucket
			// resolves silently to the wrong session on the agent side. Omit
			// it from SessionRefs; the Description's reproducible portion
			// carries the bucket-qualified ref (proj:<bucket>:<sid> via
			// doctorSel) so the session is traceable. Also omit when
			// nonReproducible (bare sid ambiguous, DoctorCommand can't
			// reproduce — disclosed in the non-reproducible section).
			if !nonReproducible && !collidingBareSid {
				f.Evidence.SessionRefs = appendUniqueString(f.Evidence.SessionRefs, agentSel)
			}
			// DoctorCommand carries doctor-CLI-safe selectors (finding 3):
			// followSelector's output (canonical proj:, safeTokenForRepro
			// proj:, or bare sid). Reproducible selectors go into
			// doctorRefsBySig for --sessions; non-reproducible sessions
			// (bare id ambiguous across buckets) go into nonReproBySig
			// with bucket context for the disclosure comment (finding 2).
			if nonReproducible {
				if nonReproBySig[sig] == nil {
					nonReproBySig[sig] = map[string]nonReproSession{}
				}
				if _, exists := nonReproBySig[sig][sessionKey]; !exists {
					nonReproBySig[sig][sessionKey] = nonReproSession{
						bucket: paths.ProjectID,
						sid:    paths.SessionID,
					}
				}
			} else {
				doctorRefsBySig[sig] = appendUniqueString(doctorRefsBySig[sig], doctorSel)
			}
		}
	}

	for _, sig := range signatureOrder {
		f := findingsBySignature[sig]
		check := checkBySignature[sig]
		// True distinct session count (round 7 finding 2): sessionsBySig
		// tracks each session by (projectID, sessionID), so two sessions
		// sharing a SID across different buckets each count. len(SessionRefs)
		// may be smaller because the bare sid dedups distinct sessions
		// sharing a SID across non-canonical buckets — TotalSessionRefs and
		// Summary.Sessions use trueCount so a consumer's reconciliation
		// stays clean.
		trueCount := len(sessionsBySig[sig])
		// The structured ref list gets the same disclosed structural cap as
		// the prose: an envelope carrying a fleet-wide Finding would otherwise
		// overflow any output limit mid-JSON. The true count is disclosed in
		// TotalSessionRefs and in Description's count. TotalSessionRefs uses
		// trueCount (not len(SessionRefs)) so it agrees with Summary.Sessions
		// and Description when deduped bare sids shrink the ref list.
		refCount := len(f.Evidence.SessionRefs)
		if refCount > evidenceSessionRefCap {
			f.Evidence.TotalSessionRefs = trueCount
			f.Evidence.SessionRefs = f.Evidence.SessionRefs[:evidenceSessionRefCap]
		} else if trueCount > refCount {
			// SessionRefs was not structurally capped, but the true session
			// count exceeds the ref count (deduped bare sids). Disclose the true
			// count so consumers reconciling SessionRefs with Summary/Description
			// are not confused by the mismatch.
			f.Evidence.TotalSessionRefs = trueCount
		}
		reproRefs := doctorRefsBySig[sig]
		nonRepro := nonReproBySig[sig]
		// Description prose (round 10 findings 3+4, round 11 finding 1): one
		// shared evidenceSessionRefCap budget covers all session references —
		// reproducible refs first, then non-reproducible disclosures in the
		// remaining slots. Each portion emits its own omission marker when it
		// overflows its budget. The reproducible portion uses reproRefs
		// (doctorRefsBySig, the reproducible selectors only) — not SessionRefs,
		// which also carries non-reproducible bare sids. Using SessionRefs for
		// the reproducible portion would emit a spurious cap marker when all
		// sessions are non-reproducible (reproBudget=0 but SessionRefs has
		// entries). The command-side omission (DoctorCommand truncates reproRefs
		// independently) is disclosed separately because the command stays a
		// pure runnable line (round 9 finding 2: no # comments).
		reproCount := len(reproRefs)
		// Reproducible refs take the first portion of the budget.
		reproBudget := min(evidenceSessionRefCap, reproCount)
		nonReproBudget := evidenceSessionRefCap - reproBudget
		// Build the reproducible ref portion with its cap marker.
		reproDesc := joinSessionRefs(reproRefs, reproBudget)
		desc := fmt.Sprintf("Runbook %q check %q tripped (%s) in %d session(s)",
			runbook.Name, check.Title, conditionsSummary(check.Conditions), trueCount)
		if reproDesc != "" {
			desc += ": " + reproDesc
		}
		// Build the non-reproducible disclosure in the remaining budget.
		totalOmitted := 0
		if len(nonRepro) > 0 {
			nonReproDesc, nonReproOmitted := formatNonReproSessions(nonRepro, nonReproBudget)
			if nonReproDesc != "" {
				if reproDesc != "" {
					desc += "; not reproducible: " + nonReproDesc
				} else {
					desc += ": not reproducible: " + nonReproDesc
				}
			}
			totalOmitted += nonReproOmitted
		}
		// Disclose command-side omissions: DoctorCommand truncates reproRefs
		// at evidenceSessionRefCap independently (round 10 finding 4). When
		// reproRefs exceeds the cap, the command silently drops selectors
		// past 200. This can differ from the SessionRefs cap when dedup
		// shrinks SessionRefs (round 7 finding 2), so the command-side
		// omission is disclosed separately from the Description ref marker.
		cmdOmitted := 0
		if reproCount > evidenceSessionRefCap {
			cmdOmitted = reproCount - evidenceSessionRefCap
		}
		if cmdOmitted > 0 {
			if totalOmitted > 0 {
				desc += fmt.Sprintf("; %d more sessions omitted from command (cap %d)", cmdOmitted, evidenceSessionRefCap)
			} else {
				desc += fmt.Sprintf("; %d sessions omitted from command (cap %d)", cmdOmitted, evidenceSessionRefCap)
			}
		}
		if totalOmitted > 0 {
			desc += fmt.Sprintf("; %d more non-reproducible sessions omitted (cap %d)", totalOmitted, evidenceSessionRefCap)
		}
		f.Description = desc
		// DoctorCommand is a pure runnable command or empty (round 9 finding 2):
		// the # shell comment form is gone because cmd.exe does not treat # as
		// a comment. When some sessions are reproducible, the command carries
		// the capped reproducible refs (no # comment appended). When ALL
		// sessions are non-reproducible, the command is empty — the disclosure
		// lives in Description prose above. The reproducible refs are capped at
		// evidenceSessionRefCap (round 7 finding 1); the cap count is disclosed
		// in Description, not in the command.
		var cmd string
		if len(reproRefs) > 0 {
			cappedRepro := reproRefs
			if len(cappedRepro) > evidenceSessionRefCap {
				cappedRepro = cappedRepro[:evidenceSessionRefCap]
			}
			cmd = fmt.Sprintf("evener doctor audit --runbook %s --sessions %s", runbook.Name, strings.Join(cappedRepro, ","))
		}
		f.Evidence.DoctorCommand = cmd
		res.Findings = append(res.Findings, *f)
		res.Summary = append(res.Summary, AuditSummaryRow{Title: f.Title, Severity: f.Severity, Sessions: trueCount})
	}
	sort.Slice(res.Unreadable, func(i, j int) bool { return res.Unreadable[i].SessionID < res.Unreadable[j].SessionID })
	return res, nil
}

// evidenceSessionRefCap bounds how many session refs a Finding carries —
// the structured SessionRefs list, the DoctorCommand reproduction line
// (round 7 finding 1), and the Description prose (round 10 finding 3:
// one shared budget across reproducible refs and non-reproducible
// disclosures — not independent 200-entry caps for each). A fleet-wide trip
// can carry thousands of refs, which would overflow any envelope carrying
// the Finding. The cut is disclosed: TotalSessionRefs carries the true
// count, the prose appends markers for reproducible and non-reproducible
// omissions plus command-side truncation (round 10 finding 4). DoctorCommand
// carries only the capped reproducible refs (no trailing comment — round 9
// finding 2 removed # comments, which cmd.exe does not treat as comments).
const evidenceSessionRefCap = 200

// joinSessionRefs joins refs comma-separated for Description prose,
// appending an "…and N more" marker past the cap. (DoctorCommand must stay
// runnable, so it joins the capped list plainly instead. The cap here is
// the reproducible-ref portion of the shared Description budget — round 10
// finding 3: non-reproducible disclosures fill the remaining slots via
// formatNonReproSessions.)
func joinSessionRefs(refs []string, budget int) string {
	if len(refs) <= budget {
		return strings.Join(refs, ", ")
	}
	return strings.Join(refs[:budget], ", ") + fmt.Sprintf(" …and %d more", len(refs)-budget)
}

func conditionsSummary(conds []auditCondition) string {
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = fmt.Sprintf("%s %s %v", c.Metric, c.Op, c.Value)
	}
	return strings.Join(parts, " && ")
}

// auditSignature builds the "recurring audit finding" signature per
// finding-contract.md: `{runbook}:{category}:{bucket}`, extended with a
// slugified title so two checks that share a category (e.g. two different
// timeout thresholds) don't collide — the contract's own guidance ("when
// unsure, bucket broader") argues for narrower dedup here, since collapsing
// two distinct checks into one Finding would misreport which threshold
// actually fired. bucket is the ISO week the audit ran, so re-running the
// same audit later the same week still dedups against itself.
func auditSignature(runbook, category, title string, now time.Time) string {
	year, week := now.ISOWeek()
	bucket := fmt.Sprintf("%04d-W%02d", year, week)
	return fmt.Sprintf("%s:%s:%s:%s", runbook, category, slugify(title), bucket)
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func appendUniqueString(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// RenderAudit renders an AuditResult as human text: the summary table
// (pattern × session count), any manual steps and unreadable sessions —
// each surfaced explicitly, never silently dropped — and finally each
// Finding as indented JSON, since a Finding is a structured contract object
// even in human-summary mode.
func RenderAudit(r AuditResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "runbook %s — sessions_checked=%d findings=%d\n", r.Runbook, r.SessionsChecked, len(r.Findings))
	if len(r.Summary) == 0 {
		b.WriteString("(no findings — healthy)\n")
	} else {
		fmt.Fprintf(&b, "%-8s %-50s %8s\n", "severity", "pattern", "sessions")
		for _, s := range r.Summary {
			fmt.Fprintf(&b, "%-8s %-50s %8d\n", s.Severity, truncate(s.Title, 50), s.Sessions)
		}
	}
	if len(r.Manual) > 0 {
		fmt.Fprintf(&b, "%d manual step(s) (require human review, not mechanically checked):\n", len(r.Manual))
		for _, m := range r.Manual {
			fmt.Fprintf(&b, "  · %s\n", m)
		}
	}
	if len(r.Unreadable) > 0 {
		fmt.Fprintf(&b, "%d session(s) could not be read:\n", len(r.Unreadable))
		for _, u := range r.Unreadable {
			fmt.Fprintf(&b, "  · %s  (%s) — %s\n", u.SessionID, u.TranscriptRef, u.Error)
		}
	}
	if len(r.Findings) > 0 {
		b.WriteString("findings:\n")
		for _, f := range r.Findings {
			enc, err := json.MarshalIndent(f, "", "  ")
			if err == nil {
				b.Write(enc)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
