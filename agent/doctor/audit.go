package doctor

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Finding is the atomic output of a doctor audit, per
// internal/bundled/skills/doctoring-serf/references/finding-contract.md.
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
	SuggestedFix SuggestedFix    `json:"suggestedFix"`
}

// FindingEvidence is the contract's evidence object. At least one sub-field
// is populated per Finding; doctorCommand is always set.
type FindingEvidence struct {
	SessionRefs     []string `json:"sessionRefs,omitempty"`
	WatchIDs        []string `json:"watchIds,omitempty"`
	DeliveryIDs     []string `json:"deliveryIds,omitempty"`
	TranscriptTurns []int    `json:"transcriptTurns,omitempty"`
	DoctorCommand   string   `json:"doctorCommand,omitempty"`
	LogSnippets     []string `json:"logSnippets,omitempty"`
}

// SuggestedFix is the contract's routing directive: diagnosis (report-only),
// runbook (extend), or skill (heal, gated).
type SuggestedFix struct {
	Type       string `json:"type"`
	FileHint   string `json:"fileHint,omitempty"`
	SymbolHint string `json:"symbolHint,omitempty"`
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

// parseAuditFence tries to YAML-decode a fenced block as an `audit:` list,
// reporting whether it found one. A fence that isn't one (e.g. the INSPECT
// section's serf-doctor invocation) either fails to decode into the wrapper
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
		return AuditCheck{}, fmt.Errorf("missing title")
	}
	if !validSeverities[raw.Severity] {
		return AuditCheck{}, fmt.Errorf("severity %q must be one of low, medium, high", raw.Severity)
	}
	if raw.Category == "" {
		return AuditCheck{}, fmt.Errorf("missing category (see finding-contract.md)")
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
		return AuditCheck{}, fmt.Errorf("both a top-level metric/op/value and an \"all\" list were given; use one or the other")
	}
	for _, cond := range conditions {
		if cond.Metric == "" {
			return AuditCheck{}, fmt.Errorf("condition missing metric")
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

// AuditResult is one serf-doctor audit run: the runbook's mechanical checks
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
		return AuditResult{}, fmt.Errorf("--sessions and --since are mutually exclusive")
	}
	if len(opts.Sessions) == 0 && opts.Since <= 0 {
		return AuditResult{}, fmt.Errorf("either --sessions or --since must be given")
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
			refs = append(refs, s.TranscriptRef)
		}
		res.Unreadable = append(res.Unreadable, sweep.Unreadable...)
	}

	needsAPILog := runbook.needsAPILog()
	needsAPIHealth := runbook.needsAPIHealth()
	now := time.Now()
	findingsBySignature := map[string]*Finding{}
	checkBySignature := map[string]AuditCheck{}
	var signatureOrder []string

	for _, ref := range refs {
		paths, err := Locate(stateBase, ref)
		if err != nil {
			res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: ref, TranscriptRef: ref, Error: err.Error()})
			continue
		}
		health, err := TranscriptHealth(stateBase, paths.TranscriptRef)
		if err != nil {
			res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: paths.SessionID, TranscriptRef: paths.TranscriptRef, Error: err.Error()})
			continue
		}
		source := metricSource{health: health}
		if needsAPILog {
			apiRes, err := APILog(stateBase, paths.TranscriptRef, APILogOpts{SummaryOnly: true})
			if err != nil {
				res.Unreadable = append(res.Unreadable, UnreadableSession{SessionID: paths.SessionID, TranscriptRef: paths.TranscriptRef, Error: err.Error()})
				continue
			}
			source.apilog = apiRes.Totals
			source.haveAPILog = true
		}
		if needsAPIHealth {
			apiHealthRes, err := APIHealth(stateBase, paths.TranscriptRef)
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
				return AuditResult{}, fmt.Errorf("runbook %s check %q on session %s: %w", runbook.Name, check.Title, paths.TranscriptRef, err)
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
			f.Evidence.SessionRefs = appendUniqueString(f.Evidence.SessionRefs, paths.TranscriptRef)
		}
	}

	for _, sig := range signatureOrder {
		f := findingsBySignature[sig]
		check := checkBySignature[sig]
		f.Description = fmt.Sprintf("Runbook %q check %q tripped (%s) in %d session(s): %s",
			runbook.Name, check.Title, conditionsSummary(check.Conditions), len(f.Evidence.SessionRefs), strings.Join(f.Evidence.SessionRefs, ", "))
		f.Evidence.DoctorCommand = fmt.Sprintf("serf-doctor audit --runbook %s --sessions %s", runbook.Name, strings.Join(f.Evidence.SessionRefs, ","))
		res.Findings = append(res.Findings, *f)
		res.Summary = append(res.Summary, AuditSummaryRow{Title: f.Title, Severity: f.Severity, Sessions: len(f.Evidence.SessionRefs)})
	}
	sort.Slice(res.Unreadable, func(i, j int) bool { return res.Unreadable[i].SessionID < res.Unreadable[j].SessionID })
	return res, nil
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
