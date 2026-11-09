package agent

import (
	"errors"
	"regexp"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/internal/tool"
)

// errWaitRefused marks a call refused because its only purpose is to wait. The
// runtime offers no wait primitive: an agent with nothing else to do ends its
// turn and the completion wakes it.
var errWaitRefused = errors.New("wait-shaped call")

// waitRefusalError is the refusal's error. Its text follows the breaker's park
// convention (tool.ParkPrefix), which other layers count refusals by.
type waitRefusalError struct{ text string }

func (e waitRefusalError) Error() string        { return tool.ParkPrefix + e.text }
func (e waitRefusalError) Is(target error) bool { return target == errWaitRefused }

func (s *Session) waitRefusal(what string) error {
	return waitRefusalError{what + " " + tool.EndTurnToWait(s.resultToolName(), s.hasHumanPartnerToAsk())}
}

// jobStatusPolls remembers, per target, the state a job_status call last
// reported. A repeat call on a running target that reports the same state
// carries no news, so it is a wait in disguise.
type jobStatusPolls struct {
	mu   sync.Mutex
	last map[string]string
}

// repeatsWithoutNews records state as target's latest reported state and
// reports whether it equals the previous one.
func (p *jobStatusPolls) repeatsWithoutNews(target, state string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]string{}
	}
	prev, seen := p.last[target]
	p.last[target] = state
	return seen && prev == state
}

// forgetDelegates drops every delegate's remembered state, for an attention
// report about a delegate.
func (p *jobStatusPolls) forgetDelegates() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for target := range p.last {
		if strings.HasPrefix(target, "dlg_") {
			delete(p.last, target)
		}
	}
}

// forget drops target's remembered state; the next job_status call on it is
// treated as a first call. A notification about the target is news, so it
// resets the memory.
func (p *jobStatusPolls) forget(target string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.last, target)
}

// jobStatusPollState is the state a running target's job_status result reports
// apart from its ever-advancing clocks, or "" when the target is not running
// (a finished target is always worth reading).
func jobStatusPollState(state any) string {
	switch st := state.(type) {
	case jobStatusResult:
		if st.RunningForMS == nil {
			return ""
		}
		return st.Status + "/" + st.Phase
	case delegateStatusResult:
		if st.RunningForMS == nil {
			return ""
		}
		return st.pollState
	}
	return ""
}

// refuseRepeatedJobStatus turns a job_status result into a refusal when it
// repeats the previous call's news-free state. Results that are not a status
// projection pass through.
func (s *Session) refuseRepeatedJobStatus(target string, value any, err error) (any, error) {
	if err != nil {
		return value, err
	}
	sr, ok := value.(tool.StateResult)
	if !ok {
		return value, nil
	}
	state := jobStatusPollState(sr.State)
	if state == "" {
		s.jobStatusPolls.forget(target)
		return value, nil
	}
	if s.jobStatusPolls.repeatsWithoutNews(target, state) {
		return "", s.waitRefusal(target + " is still running and its status and phase have not changed since your last job_status call.")
	}
	return value, nil
}

var (
	sleepSegment  = regexp.MustCompile(`^(exec +|command +)?(/usr/bin/|/bin/)?sleep +([0-9]+(\.[0-9]+)?[smhd]?|infinity)$`)
	fillerSegment = regexp.MustCompile(`^(echo|printf)( +[^ ].*)?$|^(true|:)$`)
	loopHeader    = regexp.MustCompile(`^(while +(true|:)|for +[A-Za-z_][A-Za-z0-9_]* +in +[A-Za-z0-9_{}. ]+)$`)
)

// isSleepOnlyCommand reports whether command does nothing but sleep: sleep
// segments, optionally with echo/printf/true filler, or a bare loop whose body
// is only sleep. It is deliberately conservative: any construct it does not
// recognise (pipes, redirects, substitution, other commands) means the command
// may be doing real work, so it is allowed.
func isSleepOnlyCommand(command string) bool {
	if strings.ContainsAny(command, "`|<>\\()") || strings.Contains(command, "$") {
		return false
	}
	command = strings.ReplaceAll(command, "&&", ";")
	if strings.Contains(command, "&") {
		return false
	}
	command = strings.ReplaceAll(command, "\n", ";")
	sleeps, filler, loops := 0, 0, false
	for seg := range strings.SplitSeq(command, ";") {
		seg = strings.Join(strings.Fields(seg), " ")
		seg = strings.TrimPrefix(seg, "do ")
		switch {
		case seg == "" || seg == "do" || seg == "done":
			loops = loops || seg != ""
		case loopHeader.MatchString(seg):
			loops = true
		case sleepSegment.MatchString(seg):
			sleeps++
		case fillerSegment.MatchString(seg):
			filler++
		default:
			return false
		}
	}
	return sleeps > 0 && (!loops || filler == 0)
}

// refuseSleepOnlyShell refuses a shell command that only sleeps.
func (s *Session) refuseSleepOnlyShell(command string) error {
	if !isSleepOnlyCommand(command) {
		return nil
	}
	return s.waitRefusal("this command only sleeps, and it was not run. Run the work you are waiting for as a background job, then do other work or stop.")
}
