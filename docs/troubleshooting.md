# Troubleshooting

Preserve the session and workspace before retrying. A failed connection does not prove that the last command, provider request, or remote deployment did nothing. Check the recorded outcome before repeating an action that writes files, spends money, or changes a remote host.

## Start with safe evidence

Run these shell commands on the host that owns the session, using the same user and configuration environment as the hub. They do not send a model-generation request:

```sh
evener --version
EVENER_OFFLINE=1 evener providers list
EVENER_OFFLINE=1 evener models inspect openai/gpt-5.6
curl -fsS http://127.0.0.1:9180/api/health
```

Use your actual model reference for `models inspect`. The offline switch suppresses catalog refresh; it is not a network sandbox. Configured credential expressions may execute during resolution. Inspectors mask credentials, but review custom URLs and diagnostics before sharing output.

Health confirms that a hub answers, not that its model credentials work or that a task succeeded. `providers list` shows configured instances and sources; it does not validate generation access. `models inspect` explains resolution without needing a hosted key.

## Find the symptom

| Symptom | Check first | Recovery |
| --- | --- | --- |
| Browser cannot connect | Hub health, tunnel, listener address, selected host | Restore the connection; use the private authorization URL for that hub |
| Unauthorized after connecting | Correct host capability/cookie, not provider API key | Authorize the browser again, never paste its token into a report |
| Lost paired phone or leaked hub capability | Owning hub service and effective `hub_state_root` | Follow [shared-token rotation](evener-hub.md#rotate-a-leaked-token-or-revoke-a-lost-device), affecting every client sharing it |
| Hub says another hub is running | Existing supervisor and listener for the same state root | Use the existing hub or stop the exact process through its supervisor; do not delete `hub.lock` |
| Provider missing | `providers list`, credential source, required region/resource variables | Connect the instance on the execution host, not only your laptop |
| Wrong model or setting | Selected host, cwd, launch provenance, explicit default | Fix the layer that wins; see [Configuration](configuration.md) |
| Launch file ignored | Repository trust and resolver diagnostics | Review changed content, approve intentionally, fix invalid paths |
| Provider file cannot load | Exact TOML/key diagnostic | Preserve file, correct the named field or follow migration below |
| Unknown instance on resume | Persisted instance name and current provider definitions | Restore the intended instance or deliberately choose a new session/configuration |
| Unknown model on Codex | Exact instance/model reference | Choose a supported model; Codex does not synthesize unknown IDs |
| Connection refused for Ollama | Server on the execution host, URL and installed model | Follow [Ollama troubleshooting](ollama.md#troubleshooting) |
| Sandbox denied a command | Startup policy, denied path, backend | Use an allowed path or start a deliberately different session, not an in-session bypass |
| Remote host attach fails | SSH, target platform, build identity, deployment policy | Follow [remote operations](evener-hub-remote-operations.md) |

## Authentication and provider failures

**401/403:** inspect the active instance and credential source. `openai` uses an API key; `openai-codex` uses OAuth. A subscription login is not a platform API key. Endpoint overrides to a different destination stop inherited vendor key variables to avoid sending a vendor secret to a gateway. For OAuth, `evener openai status` checks local sign-in state; `evener openai login` starts a fresh sign-in. See [Connecting a provider](connecting-a-provider.md).

**Save succeeded, check failed:** the credential may already be stored. Correct the endpoint or account access, then check again. Cancel does not undo a save. A model-list check does not prove generation quota or permission for a specific model.

**Rate limit versus quota:** a transient rate-limit response can be retried according to provider retry guidance. An exhausted quota or usage limit needs account action or waiting until its reset. Repeated requests do not repair it. Context-length errors require less input, compaction, or a correctly declared context window. Unsupported request fields require correcting the provider/model configuration, not blind retries.

**Idle timeout:** the default is ten minutes without response bytes, not ten minutes total. A heartbeat resets it. A timeout or interrupted stream leaves the provider-side outcome uncertain; check the transcript and task changes before issuing the task again. Change `provider_idle_timeout` only when the endpoint's behavior justifies it.

Live checks are opt-in and may contact providers or incur charges:

- `evener providers list --check` tests reachability.
- `evener providers probe INSTANCE` can send minimal generation requests.
- `evener providers probe INSTANCE --write` also changes provider configuration.
- `evener providers add` probes by default; use `--no-probe` when authoring configuration without a live request.

Do not use those as the first diagnostic on a secret-bearing production setup.

## Configuration did not take effect

A running process does not inherit later shell exports. Restart the hub with its supervisor's intended environment, then create a new session. Do not assume reloading the browser reloads the daemon.

The hub observes provider-file edits on a five-second notice interval. If an edit is invalid, it retains the last valid configuration, shows the load error, and blocks writes that would overwrite your edited file. At startup with an invalid file it uses implicit instances only. Repair the source file and wait for the next notice; do not delete the store to clear the error.

`EVENER_PROVIDERS_CONFIG` has three states: unset uses the default file, a path selects that file, and an explicitly empty value disables the user layer. Inspect whether it is set without printing the whole environment. Launch configuration, provider configuration, and credentials have different precedence; use the [configuration guide](configuration.md), not a universal “environment wins” rule.

For old-layout or old-schema files, first preview:

```sh
evener migrate --dry-run --verbose
```

Review the reported paths privately. Before applying `evener migrate`, stop writers and back up as described in [Security](security.md#storage-and-backups). The converter preserves an old provider file as `providers.toml.pre-registry`; review conversion comments and renamed instances. Migration is not a general repair tool for every saved session.

## Inspect a saved session

Doctor reads persisted evidence. Replace `SESSION` with a session selector you obtained from the UI or listing; do not include angle brackets in the command.

```sh
evener doctor sessions --json
evener doctor locate SESSION
evener doctor transcript SESSION --format outline
evener doctor jobs SESSION
evener doctor apilog SESSION --summary
```

Replace `SESSION` with an ID or full ref from `sessions`. If an ID matches more than one bucket, use its `proj:PROJECT_ID:SESSION_ID` ref. Doctor does not accept `current`. For a narrow transcript window, use `evener doctor transcript SESSION --range last:10`. These records can contain private task content. Keep them local until reviewed. Doctor sees settled on-disk evidence, not a guarantee that a live process has finished flushing its state. The [Doctor selector reference](cli.md#doctor-session-selectors) covers discovery, selector forms, and state roots.

### When the problem is a provider call

`evener doctor apilog` reads the session's provider-attempt log, which the
transcript does not contain. Use it when a turn failed, stalled, or cost more
than expected:

```sh
evener doctor apilog SESSION --summary    # attempts, tokens, latency
evener doctor apilog SESSION --errors     # failed attempts by class
evener doctor apilog SESSION --empty      # responses that carried no content
evener doctor apilog SESSION --cache-spikes --threshold 50000
evener doctor apilog SESSION --health     # one-line verdict
evener doctor apilog SESSION --validate   # whole-history integrity scan
```

API-request logging is off by default, so the log may be empty or absent even
for a session that failed. The hub's `api_log` setting records it for sessions
it spawns; the `--api-log on` one-shot option records it for a direct run. Turn
it on before reproducing the problem, not after.

Where an attempt ended without a usable response, check whether the provider
reported quota, authentication, or a transport failure. An attempt record is not
proof that a generation happened: it records what Evener observed, and an
interrupted stream leaves the provider-side outcome uncertain.

### Other read-only inspections

These subcommands answer narrower questions and are safe to run while the
session is idle:

| Command | Use it for |
| --- | --- |
| `evener doctor tree SESSION` | Parent, delegate, and observer relationships; `--observers` includes observer edges, `--depth N` limits expansion. |
| `evener doctor watches SESSION` | Watch deliveries and provenance; `--self-loops` shows only watches where the runaway fuse fired. |
| `evener doctor mutations SESSION` | The client-mutation journal and queue, when a UI action's outcome is unclear. |
| `evener doctor turnids` | Reserved-turn-id sweep across every session, when a restore keeps failing. |
| `evener doctor plugins` | Plugin-store health. Takes no session selector. |

When an interface disconnects, reconnect to the same session before creating another. Compare the transcript, job status, filesystem changes, and any external service outcome. A shell exit code reports that command's result; it does not by itself prove or disprove the larger task. Follow [Daily work](daily-work.md) to review the result.

## Reconstruct a lost transcript

Use `evener doctor reconstruct` only when an external **AgentsView `sessions.db`**
archive contains the missing conversation and matching root-session metadata
survives. The hub's `index.db` is not this archive. Prefer a complete backup and
the [history restore rehearsal](security.md#rehearse-history-restoration) when
the original transcript is available.

Prerequisites: the session ID, a consistent private copy of the AgentsView
database, a copy of that root session's `.meta.json`, and a **nonexistent** output
directory under a private parent. Stop archive writers or use SQLite's supported
backup operation before taking the database copy; copying a live main file alone
can lose WAL contents. Preserve the originals and their checksum manifest. Work
only on copies. Forked or delegated histories and mismatched metadata are refused.

1. Set `session`, `archive_copy`, and `meta_copy` to those copied inputs. Set
   `staging` to a new path, not a live state root. Do not create `staging` first:
   the command creates it privately and refuses to overwrite it.
2. Stage the reconstruction:

   ```bash
   evener doctor reconstruct "$session" \
     --agentsview-db "$archive_copy" --meta "$meta_copy" \
     --output-dir "$staging" --json
   ```

   If the matching mutation journal survives, copy it too and add
   `--mutations "$mutations_copy"`. This recovers client input identities, not
   a runnable input queue. Stop on a nonzero exit; keep the inputs and diagnose
   the refused archive rather than editing it until it passes.
3. Inspect `report.json` in the stage. Compare `recovered_through` with
   `metadata_updated_at`: metadata can describe later work the archive lacks.
   Review `missing_tool_outputs`, `historical_attention_records`, and
   `limitations`. Verify `transcript_sha256` and `source_snapshot_sha256` against
   the staged files. The latter hashes the normalized `source-snapshot.json`,
   **not** the original database. Recheck the original and copied input hashes.
4. Read without launching an agent:

   ```bash
   evener doctor sessions --state-dir "$staging" --json
   evener doctor transcript "$session" --state-dir "$staging" --format outline
   evener doctor transcript "$session" --state-dir "$staging" --range last:10
   ```

   Require the expected session and recorded work through the reported boundary.
   Missing tool output is unavailable evidence, not proof of success or failure.
   Keep the staged transcript separate from the original state.

Reconstruction writes normalized readable history, copied metadata, a source
snapshot, and a report. It does not recover jobs, delegates, task lists, queues,
live processes, or runtime attention delivery. It cannot restore media bytes or
provider replay identifiers/signatures. Historical attention-resolution records
remain in the source snapshot, not replayable model history. Formatting and
content-part boundaries may differ from the lost transcript.

Readable staged history is not a full backup or a guarantee of safe resumption.
Check current files and external effects yourself, then use a reviewed handoff
for a new task when needed. This procedure does not install the reconstruction
over live state and does not migrate [retired legacy delegate or watch
state](../UPGRADING.md#before-shutting-down-an-older-session).

## Report a reproducible problem

Include the build version, OS, selected host, sanitized model/instance name, exact command or UI action, expected result, actual error, and whether retry could duplicate a write. Include a short, reviewed excerpt rather than an entire state directory or API log. Never include a key, cookie, authorization URL, OAuth record, or full environment dump.

For a regression after upgrade, preserve the old and new build identities and the migration preview. Do not overwrite newer state with an old backup merely to test an older executable. Test recovery in an isolated copy first.
