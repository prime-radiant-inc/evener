## Transcript format

Serf agents and subagents write session transcripts as JSONL files.
When a subagent finishes, its result includes the `transcript` path.
Each line is a JSON object.

Line 1 is the header:

```json
{"kind":"header", "session_id":"01KNZ2TCT3...", "task":"Verify the implementer's work against the full task spec..."}
```

Remaining lines are entries. An agent tool call:

```json
{"kind":"entry", "turn":{"kind":"ASSISTANT", "message":{"content":[{"kind":"tool_call", "tool_call":{"name":"list_dir", "arguments":{"path":"/app","pattern":"*"}}}]}}}
```

The tool's response:

```json
{"kind":"entry", "turn":{"kind":"TOOL_RESULTS", "message":{"content":[{"kind":"tool_result", "tool_result":{"name":"glob", "content":"/app/apply_macros.vim\n/app/input.csv\n/app/expected.csv"}}]}}}
```

Other turn kinds: `USER_INPUT`, `STEERING` (task-list messages).

To understand what an agent did: read its `ASSISTANT` turns' tool calls.
To understand what it observed: read the `TOOL_RESULTS` content.
