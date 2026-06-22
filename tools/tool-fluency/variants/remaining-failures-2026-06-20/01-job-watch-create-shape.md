# Tool Fluency Experiment: job_watch create shape

When creating a watch, call `job_watch` with `operation` and `source` at the top
level. Delivery is implicit to the session that creates the watch.

Canonical create shape:

```json
{
  "operation": "create",
  "source": "parent",
  "events": ["assistant.tool"],
  "event_filter": {"tool_name": "read_file", "status": "ok"}
}
```
