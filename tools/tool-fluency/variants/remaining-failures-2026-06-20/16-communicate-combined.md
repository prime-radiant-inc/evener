# Tool Fluency Experiment: combined communicate guidance

For final results, call `communicate` with:

```json
{
  "purpose": "final_result",
  "output": {"message": "observed data plus required result token"}
}
```

Make the visible message contain every required literal marker from the user
request.
