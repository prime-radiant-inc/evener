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
		attrs[a[1]] = a[2]
	}
	jobID = strings.TrimSpace(attrs["job_id"])
	status := strings.ToLower(strings.TrimSpace(firstNonEmptyStr(attrs["status"], attrs["event"])))
	exit := strings.TrimSpace(attrs["exit_code"])
	isError = strings.Contains(status, "fail") || status == "error" || (exit != "" && exit != "0")

	headline = communicateHeadline(m[2])
	if headline == "" && status != "" {
		headline = status
	}
	return jobID, headline, isError, true
}

// communicateHeadline parses the communicate envelope that rides after an
// "excerpt:" marker in a job-notification body into a compact headline.
func communicateHeadline(body string) string {
	idx := strings.Index(body, "excerpt:")
	if idx == -1 {
		return ""
	}
	excerpt := strings.TrimSpace(body[idx+len("excerpt:"):])
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
