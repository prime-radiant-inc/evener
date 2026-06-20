# Tool Fluency Experiment: job_watch create shape

When creating a watch, call `job_watch` with the operation and target fields at
the top level. Put delivery instructions inside `send`.

Canonical create shape:

```json
{
  "operation": "create",
  "target": "caller",
  "events": ["assistant.tool"],
  "event_filter": {"tool_name": "read_file", "status": "ok"},
  "send": {"to": "DELEGATE_ID"}
}
```
