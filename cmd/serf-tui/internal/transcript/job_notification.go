package transcript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	jobNotificationRe     = regexp.MustCompile(`(?s)^<job-notification\s+([^>]*)>(.*)</job-notification>\s*$`)
	jobNotificationAttrRe = regexp.MustCompile(`(\w+)="([^"]*)"`)
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
// on the web side, cmd/serf-hub/frontend).
func decodeNotificationEntities(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&amp;", "&")
	return s
}

// ParseJobNotificationHeadline extracts the job id, a one-line result headline
// (test summary / status · short commit · concern count), and whether it
// reported a failure, from a <job-notification> steering payload. ok=false when
// the text is not a job notification. Used to tie a notification to its rail row.
func ParseJobNotificationHeadline(text string) (jobID, headline string, isError, ok bool) {
	m := jobNotificationRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", "", false, false
	}
	attrs := map[string]string{}
	for _, a := range jobNotificationAttrRe.FindAllStringSubmatch(m[1], -1) {
		attrs[a[1]] = decodeNotificationEntities(a[2])
	}
	jobID = strings.TrimSpace(attrs["job_id"])
	status := strings.ToLower(strings.TrimSpace(firstNonEmptyStr(attrs["status"], attrs["event"])))
	exit := strings.TrimSpace(attrs["exit_code"])
	isError = strings.Contains(status, "fail") || status == "error" || (exit != "" && exit != "0")

	// Only a delegate's terminal block legitimately carries a communicate
	// envelope in its excerpt (agent/session_tools_communicate.go writes it;
	// agent/job_notify.go stamps job_type on every job block). Shell stdout
	// is literal output even when it coincidentally parses as the envelope's
	// shape — the same gate the web parser applies (steeringClassify.ts,
	// kata 9cnq; this is its TUI twin, kata sdvc).
	if attrs["job_type"] == "delegate" {
		headline = communicateHeadline(decodeNotificationEntities(m[2]))
	}
	if headline == "" && status != "" {
		headline = status
	}
	return jobID, headline, isError, true
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
