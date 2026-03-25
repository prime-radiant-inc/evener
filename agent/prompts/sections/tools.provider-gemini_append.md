## Tool selection

- **read_many_files**: Read several files in a single call. Use for batch exploration
  instead of multiple read_file calls.
- **read_file**: Read a single file.
- **list_directory**: List directory contents with optional depth. Use to explore project
  structure before diving into files.
- **grep_search**: Search file contents by regex pattern.
- **glob**: Find files by name pattern (e.g., `**/*.go`).
- **write_file**: Create a new file or overwrite entirely. Use only for new files.
- **run_shell_command**: Run commands (build, test, git). Check output for errors.
- **edit_file**: Modify existing files. Prefer editing existing files over creating new ones.
