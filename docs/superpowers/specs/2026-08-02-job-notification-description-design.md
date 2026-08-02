# Job notification description labels

## Goal

Collapsed job and delegate notifications should show the job's one-line description instead of the generic job type when that description exists.

## Design

The existing job record already stores `Description`. The notification path will carry it as a structured `description` attribute, parse it into `ParsedNotification`, and use it as the card's secondary label. The title remains status-oriented, such as `Job completed`.

The label rule is:

1. Show the trimmed one-line description when present.
2. Otherwise retain the current job-type fallback, such as `delegate` or `shell`.
3. Preserve existing exit-code and reason metadata for failures and warnings.

The expanded card, raw notification disclosure, transcript action, and output rendering remain unchanged.

## Testing

Add frontend regression coverage for a delegate notification with a description and for the no-description fallback. Add or update backend notification coverage to verify that a job description reaches the rendered notification block. Run the focused frontend tests and relevant Go notification tests.

## Isolation and integration

Implement the change in a new worktree based on the current branch. Preserve the current worktree's unrelated edits. Commit the focused implementation and tests in the new worktree, return to the current worktree, and merge the worktree branch back without discarding existing changes.
