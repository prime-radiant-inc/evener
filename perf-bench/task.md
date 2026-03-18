Build a Python CLI todo list application. All files go in the current working directory.

## Files to create

### `storage.py` — JSON file persistence

- `TodoStore` class that reads/writes a JSON file (`todos.json`)
- Each todo is a dict: `{"id": int, "text": str, "done": bool}`
- Methods: `load() -> list[dict]`, `save(todos: list[dict])`, `next_id() -> int`
- Auto-creates the JSON file if it doesn't exist (empty list)
- IDs are auto-incrementing (max existing ID + 1)

### `todo.py` — CLI entry point

Uses `argparse` with subcommands:

- `add <text>` — adds a new todo, prints `Added: <text> (id=<id>)`
- `list` — prints all todos, format: `[<id>] [x] <text>` for done, `[<id>] [ ] <text>` for not done. Prints `No todos.` if empty.
- `done <id>` — marks todo as done, prints `Completed: <text>`. Prints `Todo <id> not found.` to stderr and exits 1 if not found.
- `delete <id>` — removes todo, prints `Deleted: <text>`. Prints `Todo <id> not found.` to stderr and exits 1 if not found.
- `search <query>` — case-insensitive substring search, prints matching todos in same format as `list`. Prints `No matches.` if none found.

### `test_todo.py` — pytest test suite

Write at least 10 tests covering:

1. `test_add_single` — add one todo, verify it appears in list output
2. `test_add_multiple` — add three todos, verify all appear
3. `test_complete` — add a todo, mark done, verify `[x]` in list output
4. `test_complete_not_found` — mark nonexistent ID done, verify exit code 1
5. `test_delete` — add a todo, delete it, verify it's gone from list
6. `test_delete_not_found` — delete nonexistent ID, verify exit code 1
7. `test_search_found` — add todos, search for substring, verify match
8. `test_search_not_found` — search for string not in any todo, verify `No matches.`
9. `test_search_case_insensitive` — search with different case, verify match
10. `test_empty_list` — list with no todos, verify `No todos.`
11. `test_ids_increment` — add, delete, add again — new todo gets a higher ID
12. `test_persistence` — add a todo, create a new store instance, verify todo persists

Each test must use a temporary directory (via `tmp_path` fixture or `tempfile`) so tests don't interfere with each other. Tests should invoke the CLI via `subprocess.run(["python", "todo.py", ...], cwd=<dir>)` or by importing and calling functions directly — either approach is fine, but they must actually test the behavior described above.

## Verification

Run `python -m pytest test_todo.py -v` and all tests must pass.
