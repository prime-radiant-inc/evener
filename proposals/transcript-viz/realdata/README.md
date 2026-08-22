# Real-session dataset for viz proposals (round 4)

Three REAL evener sessions, extracted 2026-08-21 with `proposals/transcript-viz/_extract`
(canonical `agent/transcript` + `agent/schema` types) and enriched with
`evener-doctor jobs|watches|tree --json`. These are actual working sessions from
this machine — treat the content as private; do not publish without the owner's
explicit say-so.

## The three sessions

| File | Model | Shape | Turns | Span | Notes |
|---|---|---|---|---|---|
| `034B9Kg7tBDQYMBLr6nsyA.json` | k3 | orchestrator | 353 | 11.5h | 8 user prompts, 34 steering, 8 delegate children, 18 jobs. The round 1–3 viz-proposal orchestration session itself. |
| `034B9Q0chIC4ng0EoxGOpQ.json` | k3 | leaf worker | 562 | 5.5h | 3 user prompts, 35 steering, no jobs/delegates. A viz-design delegate doing focused build+verify work. |
| `034B3mp76Fsod8yMVDlj2k.json` | gpt-5.6-luna | big-tree orchestrator | 932 | 15h | 11 user prompts, 91 steering, 39 delegate children, 238 jobs. |

## Honest properties of THIS real data (design must cope)

- **No compaction events.** None of the three contain CHECKPOINT/SUMMARY turns.
  Do not invent folds; fold encodings must degrade gracefully to absence.
- **No watches fired.** `watches` is empty in all three.
- Session B has **zero jobs and zero children** — a leaf worker must not look broken.
- Steering is frequent and meaningful (91 steering turns in session C).
- Timestamps are real: multi-hour idle gaps exist and must render as gaps.

## Schema (per file)

```jsonc
{
  "id": "034B…", "model": "k3", "parent": "…", "depth": 0,
  "started": "…", "ended": "…", "turns": 353,
  "stream": [ {
    "i": 0,                       // turn index
    "kind": "USER_INPUT",         // USER_INPUT | STEERING | ASSISTANT | TOOL_RESULTS | ENVIRONMENT | SYSTEM | HOOK_COMPLETED | ATTENTION_RESOLUTION | CHECKPOINT | SUMMARY
    "ts": "2026-08-21T19:04:…",   // real timestamp
    "in": 1234, "out": 567,       // assistant turns only: token usage
    "text": "…",                  // user/steering ≤400 chars; assistant/system ≤200 chars
    "calls": [ {"n": "shell", "t": "git status", "e": true} ],  // assistant turns; e = errored
    "res_bytes": 4096,            // TOOL_RESULTS turns: rough result size
    "steer_src": "user"           // STEERING only: user-sent vs daemon nudge
  } ],
  "jobs": [ {"id":"job_…","type":"shell","status":"completed","reason":"exit_zero",
             "exit":0,"started":"…","ended":"…","cmd":"…","out_bytes":266469} ],
  "watches": [],                  // empty in all three sessions
  "children": [ {"session":"034B…","agent_type":"default","status":"idle","edge":"delegate"} ]
}
```
