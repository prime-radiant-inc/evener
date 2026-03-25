## Tool selection

- **grep**: Search file contents by regex. Use to find definitions, references, and patterns.
- **glob**: Find files by name pattern (e.g., `**/*_test.go`). Use to discover file layout.
- **read_file**: Read a specific file. Always read before editing.
- **write_file**: Create a new file or overwrite entirely. Use only for new files.
- **shell**: Run commands (build, test, git). Check output for errors.
- **edit_file**: Modify existing files. Prefer edit existing files over creating new ones.
