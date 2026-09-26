# Session interrogation: the WHY that durable state cannot hold

Transcripts, api logs, and jobs answer WHAT a session did. Two questions they
cannot answer: **could the model see X** (as delivered through the provider
gateway, not merely as the client sent it), and **why did it make (or skip) a
choice**. Interrogation resumes the real session — on a copy — and asks the
model directly, with `--api-log on` so the wire evidence lands beside the
account. It is a live evidence source, the one deliberate exception to the
read-only posture, and it never touches the original corpus.

## When it is the right tool

- A candidate mechanism never fired in live rollouts, and exposure versus
  adoption is the open question (proven case below).
- A trajectory repeats a surprising choice that token counts cannot explain.
- You need proof that a schema property or prompt text crossed the provider
  gateway intact — neither the transcript (it records no request bodies) nor
  a fresh session (different history) can show that.

## Procedure

1. **Copy first. NEVER resume a collected corpus in place.** Wave transcripts
   and measurement state are inputs you cannot regenerate. A second reason is
   mechanical: the scratch-retention manifest pins the session's original
   state-dir path, so a full `--resume` of a moved copy is refused by design.
   Copy, then work in the copy:

       cp -r <state-dir> <scratch>/interrogate-<name>-state

2. **Identify the session** in the copy:

       evener --state-dir <copy> --list-sessions

3. **Resume the context in a NEW session with wire logging on**, bounded:

       <env with provider key + EVENER_PROVIDERS_CONFIG> \
         evener --state-dir <copy> --resume-with <session-id> \
         --api-log on --max-rounds 2 --model <provider/model> '<question>'

   `--resume-with` seeds a new session from the old transcript (no
   retained-scratch restore, so the moved copy works); the model keeps the
   full history in context. Use the session's own provider/model so the
   account comes from the same model that made the choices.

4. **Ask in three numbered parts, and forbid tool use in the prompt itself:**
   1. Recite the relevant tool's parameters exactly as declared in your tool
      schema, with each description as you understand it.
   2. Name the affordance in question and quote what its description says.
   3. Why did you make (or not make) that choice in that session? Press for
      honest, specific reasoning, and ask which reasons are principled and
      which are habit.

5. **Read both artifacts, not just the answer.** The reply is the model's
   self-account. The new `<copy>/sessions/<id>.api.jsonl` holds the actual
   wire request (tool schemas included) and response — grep the schema bytes
   you care about.

6. **Attribute carefully in whatever Finding consumes this.** A verbatim
   recitation proves delivery: the model cannot recite what it never
   received. The self-account is evidence about what the model believed and
   why; the api log is ground truth about the wire. Cite the two separately,
   and corroborate any load-bearing claim in the account against the api log
   or transcript before emitting a Finding — the account can confabulate.

## Worked example: the run_after adoption finding (2026-09-20)

The SoL-Pi loop's Action Fusion mechanism (an optional `run_after` command on
the file-mutation tools) was adopted 0 times across ~1,225 candidate requests
on deepseek-4.1-flash-background. Offline tests proved the harness half; the
session metas proved the flag was on; neither proved what the model saw or
why it declined. Interrogating the real candidate session, resumed on a copy,
returned: a verbatim recitation of the full `run_after` description
(gateway pass-through confirmed in one live call), and an account of zero
adoption as habit plus a genuine workflow mismatch — the model batches
related edits and then runs durable multi-file test jobs, a pattern a
single fused command cannot express — with the model conceding the
quick-check cases were missed efficiency. A suspected wiring bug became a
model-behavior finding with wire evidence, at the cost of one live call.
