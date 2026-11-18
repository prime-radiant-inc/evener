package mergeq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
}

// ghName is the executable to run.
func (c GHClient) ghName() string {
	if c.GH == "" {
		return "gh"
	}
	return c.GH
}

// run executes gh and returns its stdout. The error carries gh's own stderr
// when it wrote one, because that text is what the operator needs to read;
// the caller adds the context that says which call failed.
func (c GHClient) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.ghName(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, errors.New(detail)
		}
		return nil, err
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
func (c GHClient) Enqueue(ctx context.Context, repo, prID string, jump bool) error {
	_, err := c.run(ctx, "api", "graphql",
		"-f", "query="+enqueueMutation,
		"-f", "id="+prID,
		"-F", "jump="+boolString(jump))
	return err
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
						Nodes []struct {
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
	if pr == nil || len(pr.TimelineItems.Nodes) == 0 {
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
