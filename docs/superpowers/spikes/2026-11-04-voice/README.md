# Voice chief-of-staff spikes, 2026-11-04

**Realtime's protocol is usable for a narrow voice bridge. A useful “Hey Evener”
detector and locked-iPhone operation have not been demonstrated.** The proposed
[v1 design](../../specs/2026-11-04-voice-chief-of-staff-design.md) separates these
facts from its release targets.

These are throwaway experiments and a source/test investigation. No production
voice implementation, provider configuration change, app replacement or TestFlight
upload is part of this record. All speech sent to OpenAI was synthetic, nonprivate
Flite output. No raw audio, models, credentials or installed dependencies are
retained here.

## Findings at a glance

| Question | Evidence | Result and limit |
|---|---|---|
| Can current OpenAI voice endpoints run? | [Hosted probe](hosted/README.md), [events](hosted/results.json), [derived summary](hosted/summary.json) | Live and Realtime Mini completed real WebSocket sessions. Full Realtime was inventoried only. |
| Can the provider expose a usable request boundary? | Same synthetic Alpha request and Beta correction | Mini produced completed Alpha/Beta function arguments. Live delegations had no task text or completed turn, so its probe used the fixture schedule. Neither connected to Evener. |
| Can output be interrupted? | Mini cancellation and truncation events | Cancel acknowledged in 88.3 ms, truncation in 90.6 ms at a **simulated** 470 ms played offset. No sound device or acoustic measurement. |
| Can generated speech read canonical text exactly? | Returned transcripts versus supplied fixture | Both paraphrased. A “read verbatim” instruction did not establish exact reading. |
| Does the proposed wake phrase work locally? | [Wake probe](wake/README.md), [57-clip manifest](wake/results.json) | Zero detections in 10 synthetic user positives and 5 identical-playback controls. All 42 negatives ran. Official LIGHT UP calibration worked. Phrase is unqualified. |
| Can the iPhone's intended native graph build/run? | [Native probe](ios/README.md), [native results](ios/evidence/results.md) | Swift 6 compiled and linked iOS/simulator targets. A disarmed simulator exercised stop-generation control. The audio graph was never initialized. |
| Does Evener already have useful durable seams? | Existing targeted tests below | Input replay, restore/steering and commit-before-output checks passed independently of voice. |
| Do manager tools run? | Five tests on the manager feature revision | Real local hub/daemon question and approval loops passed with scripted LLM calls. This code is absent from the design baseline. |

### Measured hosted sample, not a model ranking

There is **one analyzed sample per model**. Two earlier pilots are retained and
excluded for instrument errors: Live's correction started on a packet before
speech, and Mini initially used an invalid per-item playback clock. The corrected
Mini probe uses one FIFO. No p95 or human preference claim follows from these runs.

| Metric | GPT-Live | Realtime Mini |
|---|---:|---:|
| Request end to first delegation/completed tool arguments | 903.9 ms | 1,046.4 ms |
| Request end to first PCM peak above threshold | 1,561.2 ms | 1,492.8 ms |
| Local session elapsed | 25.555 s | 17.844 s |

The PCM threshold measures signal, not speech or audibility. Input was paced and
its estimated ending differs from a microphone endpoint. Returned transcripts can
contain canceled or unplayed words. Across all four sessions, reported usage
calculates to at most **$0.0619962**, with no cache discount, within the experiment's
$1 budget. No billing invoice was checked.

### Wake result

The pinned sherpa-onnx runtime used one desktop CPU inference thread and a fixed
threshold/score chosen before inference. Processing 155.48612 seconds of independent
clips took 3.003081 process CPU seconds, RTF 0.019314. Model load took 0.944344 wall
seconds after files had been read for hashing. Neither figure predicts phone
battery or cold-storage startup. Zero false triggers is not reassuring when all
intended triggers also miss. Continuous-stream and duplicate-playback controls
also failed to establish the proposed phrase. See the wake README for calibration,
licensing uncertainty and complete reproducible commands.

### Native result

On `m5max`, the standalone native service compiled and linked for arm64 iOS and
iOS Simulator with Swift 6 and SDK 27.2. Each build ran the shared playback-fence
assertions. A private iPhone 17/iOS 27.0 simulator entered the real SwiftUI view
and rejected an old-generation synthesis callback while disarmed. Capture and
scheduled playback stayed at zero. No microphone permission was granted or audio
graph initialized. The parent checked both source-hash manifests after copying
the artifacts and reran the scripts' offline help and Bash syntax checks.

Xcode first-launch setup was incomplete: its check exited 69, and simulator tools
printed package-authorization failures even though the measured compile and
disarmed run completed. No setup repair was attempted. The physical phone was
unavailable. Signing, installation on that phone, locked capture, AEC and audible
stop latency remain untested. The experiment's private simulators and scratch
were removed after preserving the source and text/JSON evidence.

## Existing Evener behavior exercised

Design baseline: `278fb3670a29979e7e4d95b387c2a05a2f884bca`.
Manager investigation revision: `d559b65fade671a48b4e0430f5cb5a8a45e4f694`, containing
manager feature `b1d178d400e33bc23d2d83cc26216e49096d51c3`. At investigation time,
that feature was not an ancestor of the design baseline or inspected local main.
No branch was merged to manufacture a voice result.

Test bodies and real-stack fixture helpers were read before interpreting results.
These commands ran to completion and exited zero. No live LLM was used by these
Go tests. They are narrow checks, not the full repository gates.

```bash
# At the design baseline.
go test ./appwire -run 'TestNormalizeMutationInput|TestInputBearingParams' -count=1 -v

GOMAXPROCS=4 go test -p 2 ./server \
  -run '^(TestAppWireMutationResponseLossRetriesOnce|TestAppWireMutationReplayTable|TestAppWireMutationPayloadMismatchIsInvalidRequest)$' \
  -count=1 -v

GOMAXPROCS=4 go test -p 2 ./agent \
  -run '^(TestClientMutation_StartClaimedWithTranscriptRestoresRunnableWithoutDuplicateAppend|TestRestoredSteeringWakesWhenTheDaemonAttaches|TestSteerLandsWhenItsTurnAlreadyEnded|TestSteerWakesAnIdleSessionToDeliverItself|TestSteerRetryStillProvokesDelivery|TestCommunicateIsRecordedBeforeItIsAnnounced|TestAnUnrecordedCommunicateFailsAServedSessionClosed)$' \
  -count=1 -v

GOMAXPROCS=4 go test -p 2 ./cmd/evener \
  -run '^TestRunServeStopParksTheQueuedMessageUntilTheUserActs$' -count=1 -v

# In a separate isolated checkout of the manager revision, without -short.
GOMAXPROCS=4 go test -p 2 ./cmd/evener-hub \
  -run '^TestManager(ProjectionPreservesSessionProperty|HasNoToggleRPC|LegacySavedSessionRemainsOrdinary)$|^TestE2E_Manager(LocalQuestionLoop|NoBrowserSandboxApproval)$' \
  -count=1 -v
```

The scout's server/agent/serve commands also set `GOTELEMETRY=off` and a disposable
scratch `GOCACHE`; the commands above omit only those cache/telemetry settings.

| Check | What actually ran | Limit |
|---|---|---|
| AppWire inputs | Normalization/input-bearing parameter tests | Audio is not an admitted input kind; this is no audio bridge. |
| Server replay | Lost handler response and new-connection retry, replay table, conflicting payload | Response-loss simulation, not physical network/crash qualification. |
| Agent recovery/output | Persisted-state restore, steer wake/race/retry, communicate persistence and write-failure refusal | No live steering latency or voice output. |
| Serve Stop | Real serve loop and WebSocket controls with provider held until cancellation, queue parked then resumed FIFO | Command exited zero but printed two `context canceled` error logs. This is not pristine-output qualification. |
| Manager property | Persisted projection, no toggle RPC, legacy ordinary sessions stay ordinary | On separate feature revision. |
| Manager question loop | Real hub/daemon, independent worker, question/communication/completion watches, manager answer | Scripted provider, local host only. No guarantee that every live watch registration survives a crash. |
| Manager approval | Real restricted-file denial resolved without worker browser subscriber | Existing approval owner; no voice approval capability. |

Source anchors at the baseline:
`server/appwire_mutation_recovery_test.go`,
`agent/session_client_mutation_test.go`,
`agent/session_steer_always_lands_test.go`,
`agent/transcript_only_writes_test.go`,
`agent/session_fail_closed_test.go`,
`cmd/evener/serve_stop_parks_queued_message_test.go`.
At the manager revision:
`cmd/evener-hub/app_manager_mode_test.go`,
`cmd/evener-hub/e2e_manager_session_test.go`.

## Reproducing the experiments

Each subdirectory README names exact prerequisites, commands, pinned dependencies,
inputs, outcomes and limits. Read the script before running it. Use fresh scratch
output for a new experiment; do not overwrite the captured result records.

The hosted probe requires `EVENER_VOICE_LIVE=1` and a separately supplied authorized
key source before making paid requests. Its ordinary offline self-check uses no
provider, reads no real credential and creates/removes only its own temporary
fake input under `EVENER_SCRATCH_DIR`:

```bash
python3 docs/superpowers/spikes/2026-11-04-voice/hosted/probe.py --self-check
```

The parent reran this inspected self-check after copying all hosted/wake artifacts
byte-for-byte into the design tree. It exercised 11 listed checks, including
literal key copying/removal, redaction, attempt/access/billing limits, manual
response caps, single-FIFO simulated playback and Live's corrected trigger.
These offline assertions validate probe logic, not hosted model behavior.

### Artifact checks

All six Markdown documents were parsed, their 21 local links resolved, and all
five Mermaid diagrams parsed and rendered in headless Chrome. Python, JSON and
plist syntax checks completed. Native compiled-input and delivered-source hashes
matched. These are targeted artifact checks, not the full repository gates.

A default-rule Gitleaks scan reported three findings, all public SHA-256 hashes:
`tokens.txt` and `scripts/text2token.py` in the wake probe's pinned download
manifests, plus the latter hash in the retained result. The source uses these
values to verify downloaded bytes. No scan rule, hash or evidence was altered to
suppress them. The separate specification scan reported no findings. The
repository's existing rules exclude this dated-document directory, so that
exclusion was not used as evidence of a clean scan.

## Independent review

A builder review identified three design gaps: speaking durable `ask_user`
questions while locked, grounding follow-up references in the speech actually
presented, and suppressing stale speech after typed corrections. The design now
specifies each contract and its acceptance scenario. The reviewer re-read those
changes and reported no important planning questions left.

A separate evidence review reproduced the hosted costs, event timings, fixture
accounting and wake counts from the retained records. It independently found the
typed-correction gap and verified its design resolution. Neither review establishes
implemented behavior or replaces the physical-device gates.

## What the combined evidence does not establish

There is no passing microphone → wake → OpenAI → Evener manager → audible reply
journey yet. No wake engine is qualified for the proposed phrase. No human speech,
physical AEC, acoustic stop latency, locked/background iPhone, battery,
Wi-Fi/cellular handoff, browser lifetime or provider reconnect was qualified.
The tests establish separate seams; connecting them remains implementation work.
The design makes each gap an explicit acceptance scenario instead of treating
source documentation or simulator results as physical-device evidence.
