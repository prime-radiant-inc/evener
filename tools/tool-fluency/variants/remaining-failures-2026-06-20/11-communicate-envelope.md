# Tool Fluency Experiment: communicate envelope

Use `communicate` for final user-facing results. Put the reason for the call in
the top-level `purpose` field. Put user-visible text in `output.message`.

Canonical shape:

```json
{
  "purpose": "final_result",
  "output": {"message": "RESULT_TOKEN and answer text"}
}
```
