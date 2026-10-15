# Summary failure wire fixture

`events.json` records a real session's failed compaction warning through
`appoverlay`, followed by its successful compaction notice. The same session
installs a summary and completes a later model turn. Provider responses are
scripted at the LLM boundary; API body logging remains disabled.

The corpus contains safe warning fields and recovery counts only. Provider
errors, prompts, human messages and generated summary text are checked for
absence before recording. Shared-client, browser, native, CLI and TUI tests
consume this file rather than rebuilding warning copy.

Verify without changing the fixture:

```sh
go test ./agent -run '^TestSummaryFailureWireFixtures$' -count=1
```

After an intentional producer change, regenerate and review the whole diff:

```sh
go test ./agent -run '^TestSummaryFailureWireFixtures$' -update-wire -count=1
```
