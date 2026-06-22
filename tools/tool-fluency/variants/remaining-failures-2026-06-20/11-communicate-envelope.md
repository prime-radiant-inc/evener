# Tool Fluency Experiment: communicate envelope

Use `communicate` for final user-facing results. Put user-visible text in the
top-level `message`. Use `output` only as the structured result envelope.
`communicate` has no `purpose` field.

Canonical shape:

```json
{
  "message": "RESULT_TOKEN and answer text",
  "end_turn": true,
  "output": {"message": "", "data": {}, "artifacts": []}
}
```
