package mergeq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/execsupport/procgroup"
)

// GHClient answers the queue's questions with the GitHub CLI: one `gh api
// graphql` per question, plus `gh pr view` and `gh run list`. It is glue —
// argument construction and wire decoding — and the decoding half is split out
// into the parse functions below so the shape GitHub actually answers with is
// pinned by a test, without a second `gh` anywhere.
type GHClient struct {
	// GH is the gh executable. Empty runs the `gh` on PATH: the merge queue
	// is GitHub's, so there is no second client to substitute for it.
	GH string
	// GroupFile is where the live gh process group is published for the
	// launcher's forced stop. Empty reads ghGroupFileEnv, which is how
	// scripts/ops/merge-enqueue.sh hands it down; a test sets it directly so the
	// handle needs no process-global environment.
	GroupFile string
}

// groupFile is the handle path this client publishes in.
func (c GHClient) groupFile() string {
	if c.GroupFile != "" {
		return c.GroupFile
	}
	return os.Getenv(ghGroupFileEnv)
}

// ghName is the executable to run.
func (c GHClient) ghName() string {
	if c.GH == "" {
		return "gh"
	}
	return c.GH
}

// ghDrainGrace bounds how long a gh that has exited successfully may keep the
// caller waiting for its captured pipes. A `gh` that is a wrapper script
// starting the real CLI without `exec` hands the pipe to that CLI, so without
// this bound cmd.Run waits for the descendant and a cancelled wait reports its
// 130 minutes late. It is short on purpose: the answer is already in the buffer
// by the time the child exits, so the grace only covers a descendant that
// lingers after writing. One second stays well under the helper's measured 0.7s
// interrupt path even with process creation on a loaded runner.
const ghDrainGrace = time.Second

// ghGroupFileEnv names the file this client publishes its live gh process group
// in. scripts/ops/merge-enqueue.sh hands the path down and reads it when it has
// to stop a helper that did not act on SIGTERM.
//
// The handle is needed because a process tree is not enough. A `gh` that is a
// wrapper starting the real CLI without `exec` and then exiting reparents that
// CLI out of this process's tree, so a launcher walking descendants from the
// helper cannot see it, while the CLI is still submitting the enqueue. The group
// id does not move when the wrapper dies, so publishing it is what lets the
// launcher reach the call after the tree that started it is gone.
const ghGroupFileEnv = "EVENER_MERGEQ_GH_GROUP_FILE"

// publishGHGroup records pid as the live gh group, and clearGHGroup forgets it.
// A group is published only while a call is in flight, so a launcher that reads
// the file is reading the group of the call this helper is waiting on.
//
// Both are best effort: a handle that cannot be written costs the launcher the
// fallback for a wrapper's orphan, and that is not a reason to fail a GitHub
// call the queue can still answer.
func publishGHGroup(path string, pid int) bool {
	if path == "" || pid <= 1 {
		return false
	}
	// Written to a fresh file in the same directory and renamed over the handle:
	// the launcher may read the handle at any moment, so the only write it can
	// observe is a whole one.
	//
	// CreateTemp, not a name of this function's choosing. The first version wrote
	// `path + ".tmp"`, which is predictable: on a shared directory another local
	// user could pre-create that name as a symlink to a file the operator can
	// write, and this would truncate it. CreateTemp opens with O_EXCL, so an
	// existing name -- a planted link included -- makes it pick another, and the
	// rename then replaces the handle's own name rather than following it.
	//
	// scripts/ops/merge-enqueue.sh already puts the handle in `mktemp -d` output.
	// This is the second lock on the same door, because a caller can hand the
	// client any path and the publisher is the last step before a write.
	file, err := os.CreateTemp(filepath.Dir(path), ".gh-group-*")
	if err != nil {
		return false
	}
	temp := file.Name()
	_, writeErr := file.WriteString(strconv.Itoa(pid) + "\n")
	// The mode is applied through the descriptor, before the close, and never by
	// name afterwards. `os.Chmod(temp, ...)` after the close is a second lookup of
	// a name this function chose inside a directory it does not own: on a shared
	// directory whose parent entries another user can replace, that lookup can
	// resolve to a link they planted, and the chmod then changes the mode of a
	// file the operator owns. The descriptor cannot be redirected -- it names the
	// inode CreateTemp opened -- so the permission this function means to set is
	// the one it sets.
	chmodErr := file.Chmod(0o600)
	closeErr := file.Close()
	if writeErr != nil || chmodErr != nil || closeErr != nil {
		_ = os.Remove(temp)
		return false
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return false
	}
	return true
}

// clearGHGroup removes the handle once the call it named has ended. It is called
// only for a handle this client published: path came from configuration, and a
// caller that pointed it at something that already existed would otherwise have
// this remove it. A failed publication leaves the name alone -- it is not ours.
func clearGHGroup(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(filepath.Clean(path))
}

// run executes gh and returns its stdout. The error carries gh's own stderr
// when it wrote one, because that text is what the operator needs to read;
// the caller adds the context that says which call failed.
//
// gh runs in a process group of its own, and a cancelled context kills that
// whole group. Closing the captured pipes is not enough on its own: a `gh` that
// is a wrapper starting the real CLI without `exec` keeps running after the
// wrapper dies, so the helper would report 130 while the CLI was still
// submitting the enqueue. The group kill is what makes "the wait stopped" mean
// the request stopped, not just that this process stopped waiting for it.
// ErrIncompleteReply is a call whose captured output was cut off by the drain
// bound before the reply arrived.
//
// It exists because the drain bound cannot tell the two cases apart on its own.
// orphanpipe.ChildErr reads ErrWaitDelay after a zero exit as the success it
// usually is: the child answered, and only a descendant it left behind kept the
// pipe open. That reasoning holds for output the child finished writing. It
// does NOT hold when the child exited before writing anything -- a gh wrapper
// that backgrounds the real CLI and returns. There is then no answer at all,
// and reporting success would tell the caller an enqueue mutation landed on a
// reply nobody received.
// ErrIncompleteReply reports a call whose reply was cut off by the drain bound
// before it arrived. It is exported because the caller has to do something
// different with it: GitHub may have accepted the mutation before the reply was
// lost, so a run must read the state back before calling the call a failure.
var ErrIncompleteReply = errors.New("gh exited before its reply arrived, and a descendant it left behind kept the pipe open past the drain bound")

// RefusalError is a call that COMPLETED and was refused: GitHub answered the
// request, and the answer was no.
//
// It is a distinct type because the run has to tell a refusal from an unknown
// ending, and the context cannot make that distinction. A refusal can arrive
// just before a budget expires, with the drain crossing it afterwards, so the
// call's context is expired while its answer is a definite no: reconciling that
// as uncertain spends a read looking for this invocation's work and finds
// whatever is there, including an entry another operator made for the same pull
// request. The answer is an answer.
//
// What makes it a refusal is GitHub's own reply, not the exit status and not
// stderr. `gh` prints a rejected GraphQL request's body on stdout, with an
// `errors` array, and copies the first message to stderr. Nothing else does: a
// transport failure, an auth failure before the request is made, and a kill all
// leave stdout empty and put only a diagnostic on stderr. A wrapper that prints
// such a diagnostic and is then killed by the enqueue deadline is therefore not
// refusing anything -- GitHub may have accepted the mutation before the kill --
// and calling it a refusal skips the read that would find out, leaving the
// operator told the enqueue failed for a pull request that is queued.
//
// Three endings, not two: refused (GitHub said no), succeeded, and unknown (no
// usable answer). Only the last is reconciled.
type RefusalError struct {
	Detail string
}

func (e *RefusalError) Error() string { return e.Detail }

// ErrOutcomeUnknown reports a call that ended without establishing whether
// GitHub performed the write: a transport failure, an auth failure before the
// request was made, or a process killed mid-call. GitHub may have accepted the
// mutation before the connection broke, so the caller must read the state back
// rather than report a failure.
//
// It is the counterpart of RefusalError, and the two are what the three endings
// are expressed in: a RefusalError is GitHub's own no, ErrOutcomeUnknown is no
// answer at all, and an error that is neither is a failure the call stated.
var ErrOutcomeUnknown = errors.New("the call ended without establishing whether GitHub performed the write, so its outcome has to be read back")

// completeReply reports whether out is a whole JSON document.
//
// Every call this client makes asks for JSON: `gh api graphql` answers in it,
// and the `gh pr view`/`gh run list` calls pass --json. A reply the drain bound
// cut off mid-write is therefore invalid JSON, and one that never started is
// empty. Both are incomplete, which is the distinction the drain bound cannot
// make by itself.
func completeReply(out []byte) bool {
	return len(out) > 0 && json.Valid(out)
}

// graphqlRefusal reports whether out is a reply in which GitHub refused the
// request, and the message it gave.
//
// This is the positive evidence a refusal needs. `gh api graphql` prints the
// response body whatever the status, so a mutation GitHub declined -- a protected
// branch, a PR another operator already queued under --jump -- arrives as
// `{"data":{...},"errors":[{"message":"..."}]}` with the message copied to
// stderr. A request that never reached GitHub has no body at all: a reset
// connection, a proxy failure and a killed process all leave stdout empty.
//
// The distinction is load-bearing in one direction only. Treating a body-less
// failure as a refusal reports a refused mutation when it may have succeeded,
// which is the misreport this exists to prevent; treating a refusal as unknown
// costs one status read and finds the truth. So the test is the errors array, and
// anything else -- including a body with data and no errors, which the caller's
// parser rejects on its own terms -- stays unknown.
//
// An errors array is not by itself a refusal either, which is why the entries
// are read for their shape and not only their message. GitHub answers a rejected
// mutation at the REQUEST level: the entry carries its own `type` (UNPROCESSABLE,
// FORBIDDEN, NOT_FOUND) and no `path`, because nothing was executed to reject.
// An entry with a `path` is a FIELD-RESOLUTION error: GitHub ran the mutation and
// then failed while building the reply, which is precisely the case where the
// write may already have landed. An entry with neither a type nor a path is an
// execution failure with no structured evidence about the request at all. Both
// of those are unknown, and unknown is read back; only the first is an answer.
//
// The previous rule took any nonempty message as the answer, so a payload that
// failed to resolve AFTER the enqueue took effect was reported as a refusal and
// the read that would have found the PR queued was skipped.
func graphqlRefusal(out []byte) (string, bool) {
	if !completeReply(out) {
		return "", false
	}
	var body struct {
		Errors []struct {
			// Type is GitHub's own classification of a request-level rejection.
			Type    string
			Message string
			// Path names the response field an execution error happened in. Its
			// presence is what distinguishes "we failed to build the reply" from
			// "we declined the request".
			Path []json.RawMessage
		}
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return "", false
	}
	for _, e := range body.Errors {
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			continue
		}
		if len(e.Path) > 0 {
			continue
		}
		if strings.TrimSpace(e.Type) == "" {
			continue
		}
		return msg, true
	}
	return "", false
}

func (c GHClient) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.ghName(), args...)
	cmd.SysProcAttr = procgroup.SysProcAttr()
	// Kill, not Terminate: this is a cancellation, and the same choice the
	// config value evaluator makes for the same reason. A wrapper's CLI that
	// ignores TERM would otherwise cost the whole drain grace before dying,
	// and a cancelled wait has nothing left to negotiate.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			procgroup.Kill(cmd.Process.Pid)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = ghDrainGrace
	// Started rather than Run so the group can be published before the call can
	// outlive its own wrapper: a cancellation arriving in the window between the
	// fork and the publish would leave the launcher with no handle for a CLI the
	// wrapper had already reparented.
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	groupFile := c.groupFile()
	// Only a handle this call published is removed when the call ends. The path is
	// configuration, and a publication that failed has left whatever was there
	// untouched: removing the name then deletes something this call did not create.
	// A directory named at EVENER_MERGEQ_GH_GROUP_FILE was the sharp case -- the
	// rename failed and the deferred removal deleted it.
	if publishGHGroup(groupFile, cmd.Process.Pid) {
		defer clearGHGroup(groupFile)
	}
	rawErr := cmd.Wait()
	err := orphanpipe.ChildErr(cmd, rawErr)
	// The group dies with the call whether or not the context fired: on a clean
	// exit nobody ran Cancel, and a wrapper's CLI would otherwise survive it.
	// After the reap the pid names no process this call owns, and the group
	// still holds whatever the call left in it.
	if cmd.Process != nil {
		procgroup.KillGroupAfterReap(cmd.Process.Pid)
	}
	// The drain bound fired on a call that left nothing decodable behind. The
	// child exited zero, so ChildErr read that as success, and a caller would
	// then report an enqueue that never happened. Only the drain case is
	// reclassified: a non-zero exit keeps its own error, and so does a call that
	// answered in full with a descendant merely holding the pipe.
	if errors.Is(rawErr, exec.ErrWaitDelay) && !completeReply(stdout.Bytes()) {
		return nil, ErrIncompleteReply
	}
	if err != nil {
		// Refused, or unknown? GitHub's reply decides, and neither stderr nor the
		// exit status can.
		//
		// A rejected GraphQL request answers with a body: `{"data":...,"errors":[...]}`
		// on stdout, with gh copying the first message to stderr. That is the only
		// ending that establishes the answer is no. A transport failure -- a reset
		// connection, a DNS failure, a TLS error -- leaves stdout empty and writes a
		// diagnostic to stderr; GitHub may still have performed the mutation before
		// the connection broke, so the outcome is unknown and has to be read back.
		// An exit status of 1 is common to both, which is why the exit status is not
		// the test, and a wrapper's own diagnostic on stderr is not a GraphQL body.
		//
		// The previous version tested the exit status after stderr was non-empty.
		// That called a transport failure a refusal and reported an enqueue GitHub
		// had accepted as a failure, which is the misreport this whole PR exists to
		// stop.
		detail := strings.TrimSpace(stderr.String())
		if refusal, ok := graphqlRefusal(stdout.Bytes()); ok {
			if detail == "" {
				detail = refusal
			}
			return nil, &RefusalError{Detail: detail}
		}
		if !completeReply(stdout.Bytes()) {
			// No body, so nothing establishes whether GitHub performed the write.
			// It is reported as unknown rather than as a failure, because a failure
			// is an answer and this call does not have one: reporting it as a
			// failure is what let an enqueue GitHub had accepted be reported as
			// refused, with the read that would have found it skipped.
			if detail != "" {
				return nil, fmt.Errorf("%w: %w: %s", ErrOutcomeUnknown, err, detail)
			}
			return nil, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
		}
		// A whole JSON body that is not a GraphQL refusal is not an observation
		// about the queue either. `gh` prints the response body on stdout
		// whatever the status, and not every body it prints is GitHub answering
		// the request: a gateway in front of the API answers 502 with its own
		// JSON diagnostic -- `{"message":"Server Error","status":"502"}` -- and
		// the CLI copies it to stdout and exits non-zero exactly as it does for a
		// real reply. That body is decodable, which is why the drain bound does
		// not call it cut off, and it names no error, which is why graphqlRefusal
		// does not call it a refusal. Reading the exit status as the answer
		// reported the enqueue failed for a mutation the request may already have
		// performed, and skipped the read that would have found it.
		//
		// So the body's presence is not the test; the refusal is. Anything short
		// of GitHub saying no leaves the outcome unknown, and unknown is read
		// back.
		if detail != "" {
			// The diagnostic is still carried: it is what the operator needs
			// when the read-back finds nothing. The error itself stays wrapped so
			// errors.Is still sees the termination cause.
			return nil, fmt.Errorf("%w: %w: %s", ErrOutcomeUnknown, err, detail)
		}
		return nil, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	return stdout.Bytes(), nil
}

// statusQuery reads the pull request's own state, its merge commit, its base
// branch and its queue entry in one query, so all four are read at the same
// instant.
const statusQuery = `query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){id state baseRefName mergeCommit{oid} mergeQueueEntry{position state}}}}`

// enqueueMutation adds the pull request to its base branch's merge queue. The
// mutation carries the jump flag, which is why a PR already in the queue has
// no way to jump.
const enqueueMutation = `mutation($id:ID!,$jump:Boolean!){enqueuePullRequest(input:{pullRequestId:$id,jump:$jump}){mergeQueueEntry{position}}}`

// dropQuery reads the latest queue removal event and the queue's own reason
// for it. That reason distinguishes a failed check from a timeout even though
// merge-group checks never report on the member PR.
const dropQuery = `query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){timelineItems(last:1,itemTypes:[REMOVED_FROM_MERGE_QUEUE_EVENT]){nodes{... on RemovedFromMergeQueueEvent{createdAt reason}}}}}}`

// splitRepo splits OWNER/NAME as the status and drop queries need it.
func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repo %q is not OWNER/NAME", repo)
	}
	return owner, name, nil
}

// Status implements Client.
func (c GHClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return Status{}, err
	}
	out, err := c.run(ctx, "api", "graphql",
		"-f", "query="+statusQuery,
		"-f", "o="+owner, "-f", "n="+name, "-F", "p="+pr)
	if err != nil {
		return Status{}, err
	}
	return parseStatus(out)
}

// parseStatus decodes the status query's answer.
func parseStatus(out []byte) (Status, error) {
	var body struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					ID              string
					State           string
					BaseRefName     string
					MergeCommit     *struct{ OID string }
					MergeQueueEntry *struct {
						Position int64
						State    string
					}
				}
			}
		}
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return Status{}, fmt.Errorf("decode status: %w", err)
	}
	pr := body.Data.Repository.PullRequest
	if pr == nil {
		return Status{}, errors.New("the repository has no such pull request")
	}
	st := Status{ID: pr.ID, State: pr.State, Base: pr.BaseRefName}
	if st.Base == "null" {
		st.Base = ""
	}
	if pr.MergeCommit != nil && pr.MergeCommit.OID != "" {
		sha := pr.MergeCommit.OID
		st.MergeCommit = &sha
	}
	if pr.MergeQueueEntry != nil {
		st.Entry = &QueueEntry{Position: pr.MergeQueueEntry.Position, State: pr.MergeQueueEntry.State}
	}
	return st, nil
}

// MergedCommit implements Client.
func (c GHClient) MergedCommit(ctx context.Context, repo, pr string) (string, bool, error) {
	out, err := c.run(ctx, "pr", "view", pr, "--repo", repo, "--json", "state,mergeCommit")
	if err != nil {
		return "", false, err
	}
	return parseMergedCommit(out)
}

// parseMergedCommit decodes `gh pr view --json state,mergeCommit`. A state
// other than MERGED is not an error and not a merge: the caller keeps waiting.
func parseMergedCommit(out []byte) (string, bool, error) {
	var body struct {
		State       string
		MergeCommit *struct{ OID string }
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return "", false, fmt.Errorf("decode pr view: %w", err)
	}
	if body.State != mergedState {
		return "", false, nil
	}
	sha := absent
	if body.MergeCommit != nil && body.MergeCommit.OID != "" {
		sha = body.MergeCommit.OID
	}
	return sha, true, nil
}

// Enqueue implements Client.
func (c GHClient) Enqueue(ctx context.Context, repo, prID string, jump bool) (*QueueEntry, error) {
	out, err := c.run(ctx, "api", "graphql",
		"-f", "query="+enqueueMutation,
		"-f", "id="+prID,
		"-F", "jump="+boolString(jump))
	if err != nil {
		return nil, err
	}
	return parseEnqueuedEntry(out), nil
}

// parseEnqueuedEntry reads the queue entry the mutation's reply names, and nil
// when it names none.
//
// The mutation asks for `enqueuePullRequest{mergeQueueEntry{position}}`, so a
// reply that performed the write carries the position GitHub put the pull
// request in. That is the one fact the confirmation read cannot recover if the
// entry is gone by the time it runs, so it is decoded here and handed to the
// caller rather than discarded.
//
// A reply this function cannot decode is not an error: the mutation returned no
// error, so the write is performed, and the confirmation read is what decides
// where it landed. Losing the entry then costs the drop diagnostics, which is
// the same position a reply that named no entry leaves the run in.
//
// The struct only accepts a QUEUED entry with a position for the same reason the
// status parser does: `mergeQueueEntry` is null in a reply where GitHub accepted
// the mutation but the pull request is not in the queue, and a zero position is
// not what that means.
func parseEnqueuedEntry(out []byte) *QueueEntry {
	var body struct {
		Data struct {
			EnqueuePullRequest *struct {
				MergeQueueEntry *struct {
					Position int64
					State    string
				}
			}
		}
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil
	}
	entry := body.Data.EnqueuePullRequest
	if entry == nil || entry.MergeQueueEntry == nil || entry.MergeQueueEntry.Position <= 0 {
		return nil
	}
	return &QueueEntry{
		Position: entry.MergeQueueEntry.Position,
		State:    entry.MergeQueueEntry.State,
	}
}

// boolString renders a flag the way gh's typed -F wants it.
func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// QueueDrop implements Client.
func (c GHClient) QueueDrop(ctx context.Context, repo, pr string) (*DropEvent, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	out, err := c.run(ctx, "api", "graphql",
		"-f", "query="+dropQuery,
		"-f", "o="+owner, "-f", "n="+name, "-F", "p="+pr)
	if err != nil {
		return nil, err
	}
	return parseDropEvent(out)
}

// parseDropEvent decodes the drop query's answer: nil when the timeline holds
// no removal event.
func parseDropEvent(out []byte) (*DropEvent, error) {
	var body struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					TimelineItems struct {
						// Pointers, so a null node stays distinguishable from a
						// node with empty fields. A null entry is what GraphQL
						// sends when the selected item is not of the requested
						// type, and the honest reading of that is "no removal
						// event", not "an event whose timestamp will not parse".
						Nodes []*struct {
							CreatedAt string
							Reason    *string
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil, fmt.Errorf("decode queue drop: %w", err)
	}
	pr := body.Data.Repository.PullRequest
	if pr == nil || len(pr.TimelineItems.Nodes) == 0 || pr.TimelineItems.Nodes[0] == nil {
		return nil, nil
	}
	node := pr.TimelineItems.Nodes[0]
	createdAt, err := time.Parse(time.RFC3339, node.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("decode queue drop createdAt %q: %w", node.CreatedAt, err)
	}
	event := &DropEvent{CreatedAt: createdAt}
	if node.Reason != nil {
		event.Reason = *node.Reason
	}
	return event, nil
}

// MergeGroupRuns implements Client: the hundred most recent merge-group runs,
// newest first, which is the order `gh run list` returns them in.
func (c GHClient) MergeGroupRuns(ctx context.Context, repo string) ([]GroupRun, error) {
	out, err := c.run(ctx, "run", "list", "--repo", repo, "--event", "merge_group", "--limit", "100",
		"--json", "databaseId,url,conclusion,headBranch")
	if err != nil {
		return nil, err
	}
	return parseRuns(out)
}

// parseRuns decodes `gh run list --json databaseId,url,conclusion,headBranch`.
func parseRuns(out []byte) ([]GroupRun, error) {
	var runs []GroupRun
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, fmt.Errorf("decode run list: %w", err)
	}
	return runs, nil
}
