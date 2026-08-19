package transcript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	// jobNotificationBlockRe extracts each individual <job-notification …>…
	// </job-notification> block. The match MUST be non-greedy and unanchored:
	// the daemon joins every job's block from one poll tick into a single
	// steering event with "\n" (agent/session_lifecycle.go), so a steering
	// payload routinely carries several blocks. A greedy or "^...$"-anchored
	// match would span from the first opening tag to the LAST closing tag,
	// swallowing every block in between into one body - degrading the first
	// job's rich headline to its bare status (the aggregated body no longer
	// parses as that job's own excerpt JSON) and leaving every later job with
	// no tie at all (issue #49). Non-greedy per-block matching is sound here
	// because the producer HTML-entity-escapes body text
	// (agent/job_notify.go's escapeNotificationText, kata 77sf), so a body
	// never contains a literal '<' that could be mistaken for another
	// block's opening tag. Mirrors the web side's splitNotificationBlocks
	// (steeringClassify.ts), which carries the same "MUST be non-greedy"
	// comment for the identical reason.
	jobNotificationBlockRe = regexp.MustCompile(`(?s)<job-notification\s+([^>]*?)>(.*?)</job-notification>`)
	jobNotificationAttrRe  = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

// decodeNotificationEntities is the paired decoder for
// agent/job_notify.go's escapeNotificationText: the producer HTML-entity-
// escapes &, <, >, and " before interpolating job/watch-derived text into a
// <job-notification> wrapper, so this parser's regex-extracted attribute
// values and body text must be decoded back before use - both for display
// (a reader should see "&", not "&amp;") and so a delegate's JSON
// communicate envelope, whose own quotes are escaped the same way, is valid
// JSON again before json.Unmarshal. &amp; is decoded LAST so double-escaped
// content only unwraps one level (mirrors NotificationCard's decodeEntities
// on the web side, cmd/evener-hub/frontend).
func decodeNotificationEntities(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&amp;", "&")
	return s
}

// JobNotificationTie is one <job-notification> block's parsed result: the job
// id it names, a one-line result headline (test summary / status · short
// commit · concern count), and whether it reported a failure. Used to tie a
// notification to its rail row.
type JobNotificationTie struct {
	JobID    string
	Headline string
	IsError  bool
}

// ParseJobNotificationHeadlines extracts every <job-notification> block's tie
// from a steering payload. A single steering event can name several jobs (the
// daemon joins one poll tick's blocks with "\n"), so callers that want to tie
// every job named in the payload - not just the first - must use this rather
// than ParseJobNotificationHeadline. Returns nil when the text carries no
// <job-notification> block.
func ParseJobNotificationHeadlines(text string) []JobNotificationTie {
	matches := jobNotificationBlockRe.FindAllStringSubmatch(strings.TrimSpace(text), -1)
	if matches == nil {
		return nil
	}
	ties := make([]JobNotificationTie, 0, len(matches))
	for _, m := range matches {
		ties = append(ties, parseJobNotificationBlock(m[1], m[2]))
	}
	return ties
}

// ParseJobNotificationHeadline extracts the first <job-notification> block's
// tie from text. ok=false when the text carries no job notification. Kept
// for the common single-job case; a payload naming several jobs (see
// ParseJobNotificationHeadlines) still returns only its first block's tie.
func ParseJobNotificationHeadline(text string) (jobID, headline string, isError, ok bool) {
	ties := ParseJobNotificationHeadlines(text)
	if len(ties) == 0 {
		return "", "", false, false
	}
	tie := ties[0]
	return tie.JobID, tie.Headline, tie.IsError, true
}

// parseJobNotificationBlock parses one already-split <job-notification>
// block's raw attribute string and body into its tie.
func parseJobNotificationBlock(attrsRaw, body string) JobNotificationTie {
	attrs := map[string]string{}
	for _, a := range jobNotificationAttrRe.FindAllStringSubmatch(attrsRaw, -1) {
		attrs[a[1]] = decodeNotificationEntities(a[2])
	}
	jobID := strings.TrimSpace(attrs["job_id"])
	status := strings.ToLower(strings.TrimSpace(firstNonEmptyStr(attrs["status"], attrs["event"])))
	exit := strings.TrimSpace(attrs["exit_code"])
	isError := strings.Contains(status, "fail") || status == "error" || (exit != "" && exit != "0")

	var headline string
	// Only a delegate's terminal block legitimately carries a communicate
	// envelope in its excerpt (agent/session_tools_communicate.go writes it;
	// agent/job_notify.go stamps job_type on every job block). Shell stdout
	// is literal output even when it coincidentally parses as the envelope's
	// shape — the same gate the web parser applies (steeringClassify.ts,
	// kata 9cnq; this is its TUI twin, kata sdvc).
	if attrs["job_type"] == "delegate" {
		headline = communicateHeadline(decodeNotificationEntities(body))
	}
	if headline == "" && status != "" {
		headline = status
	}
	return JobNotificationTie{JobID: jobID, Headline: headline, IsError: isError}
}

// communicateHeadline parses the communicate envelope that rides after an
// "excerpt:" marker in a job-notification body into a compact headline.
func communicateHeadline(body string) string {
	_, after, ok := strings.Cut(body, "excerpt:")
	if !ok {
		return ""
	}
	excerpt := strings.TrimSpace(after)
	var env struct {
		Data struct {
			Status       string   `json:"status"`
			TestSummary  string   `json:"test_summary"`
			CommitHashes []string `json:"commit_hashes"`
			Concerns     []string `json:"concerns"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(excerpt), &env) != nil {
		return ""
	}
	bits := make([]string, 0, 3)
	switch {
	case env.Data.TestSummary != "":
		bits = append(bits, clipStr(env.Data.TestSummary, 60))
	case env.Data.Status != "":
		bits = append(bits, env.Data.Status)
	}
	if len(env.Data.CommitHashes) > 0 {
		bits = append(bits, shortHash(env.Data.CommitHashes[0]))
	}
	if n := len(env.Data.Concerns); n > 0 {
		unit := "concern"
		if n > 1 {
			unit += "s"
		}
		bits = append(bits, fmt.Sprintf("%d %s", n, unit))
	}
	return strings.Join(bits, " · ")
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func clipStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func shortHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
