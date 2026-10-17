# Memory

Memory lets Evener carry useful preferences and project decisions into later sessions. It is selected knowledge, not a transcript of everything you said, and it is not authority to act against your current request.

Evener stores each topic in a file called a **memory page**. It generates an **index**, a list of page paths and short descriptions, to help the agent find relevant topics.

Use memory for context that a later session needs but cannot reliably infer from the code. Keep task status in the conversation or task list, and keep credentials out of both.

## Choose the right scope

| Scope | What belongs there | Example |
| --- | --- | --- |
| Personal | Preferences and environment facts that apply across projects on this host | You prefer reports to distinguish checks run from checks recommended |
| Project | Decisions, constraints, and conventions specific to the bound project | Why an integration must retain its existing wire format |

Personal memory is host-owned, not a cloud-synchronized account profile. Project memory follows the project's identity; linked Git worktrees share the main checkout's identity. A remote host has its own storage. Do not assume a note saved locally has appeared on another host.

## Ask Evener to remember a decision

Tell the main session what should persist and why. For example:

```text
Remember this project decision: retain the current export column order
because the accounting importer addresses columns by position. Record
the reason, not just the current order.
```

Ask for confirmation of the scope and page path when the distinction matters. A statement that the agent “will remember” is weaker evidence than an actual saved page. Not every observation deserves a note: code that can be reread, general programming advice, and the status of today's task usually belong elsewhere.

Delegates can read and search memory, but cannot write, edit, or delete its pages. They report useful findings to their parent session, which decides what to save. A read-only workspace role can still permit the main session to write memory; workspace access and memory access are separate capabilities.

## Read and correct what is saved

You can ask the main session to inspect its memory:

```text
Read the project memory about exports. Show the saved decision and its
page path, then compare it with the current implementation before using it.
```

Correct a stale note directly:

```text
The importer now reads headers by name. Update the project memory to
remove the old column-order constraint and record the new reason.
```

Evener supplies bounded personal and project indexes to the model. Topic pages are read when needed; every page is not loaded into every request. The `MEMORY.md` index is generated from each page's metadata: fields in its header that describe the topic. Editing a generated index is not how you update a topic's source content.

The transcript may show **Refreshed my memory** when the session receives updated index information. That row records an observation; it does not mean the agent reread every topic page or verified every claim. Current evidence should override an obsolete note.

## Forget a note

Ask for the specific page or fact to be removed and verify the resulting page content or deletion:

```text
Remove the obsolete export column-order rule from project memory. Keep
unrelated decisions. Report which page changed and what remains.
```

Removing a memory page prevents future reads of that stored page. It does not retract text already included in an active model context, erase historical transcripts, or delete provider-side copies. If sensitive information was saved, handle the exposure separately; deleting a note is not a substitute for rotating a leaked credential.

## Where memory lives

The default state root is `${XDG_STATE_HOME:-$HOME/.local/state}/evener`. `XDG_STATE_HOME` is an optional environment variable that selects where applications store persistent state; when unset, Evener uses `~/.local/state/evener`. Under that root:

```text
memory/                         personal pages
projects/<Project.ID>/memory/    project pages
```

`<Project.ID>` is the resolved project identifier, not a path you should guess from the repository's display name. Ask the session for its bound page path when locating a note.

Session history overrides such as `--state-dir` or `EVENER_STATE_DIR` do not relocate memory. Deleting a project's sessions does not remove its memory directory. Keep that distinction in mind when cleaning up state or moving between hosts.

These are ordinary host files, so they can be inspected and backed up. Avoid editing the same page concurrently with an agent. Do not treat copying a live state directory as a guaranteed consistent backup of all Evener state. The [configuration guide](configuration.md) and [troubleshooting guide](troubleshooting.md) cover the wider operational context.

## What memory is not

Memory does not grant shell access, network access, or permission to bypass an approval. It does not replace versioned project instructions, an extension's procedure, or an acceptance test. A saved approval is not permission for a future action.

Use [extensions](extensions.md) for reusable procedures, [daily work](daily-work.md) for tracking current tasks, and [concepts](concepts.md) to distinguish memory from transcripts and workspace files. The [memory tool reference](tools/memory.md) gives exact tool behavior, page formats, and limits.
