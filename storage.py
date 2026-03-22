from __future__ import annotations

import json
import os
import tempfile
from typing import Any


class TodoStore:
    """Simple JSON-backed storage for todos.

    Each todo is represented as a dict:
        {"id": int, "text": str, "done": bool}

    By default, data is persisted to a file named "todos.json" in the
    current working directory.
    """

    def __init__(self, filename: str = "todos.json") -> None:
        self.filename = filename

    def _ensure_file(self) -> None:
        """Create the backing JSON file if it doesn't exist."""
        if os.path.exists(self.filename):
            return
        # Create parent directory if a path was provided.
        parent = os.path.dirname(os.path.abspath(self.filename))
        if parent and not os.path.exists(parent):
            os.makedirs(parent, exist_ok=True)
        self.save([])

    def load(self) -> list[dict[str, Any]]:
        """Load todos from disk.

        Auto-creates the JSON file if missing, containing an empty list.
        """
        self._ensure_file()
        # Be tolerant of an empty file (treat as no todos), but reject other
        # malformed JSON so corruption isn't silently ignored.
        with open(self.filename, "r", encoding="utf-8") as f:
            raw = f.read()
        if raw.strip() == "":
            return []
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as e:
            raise ValueError(f"Invalid JSON in {self.filename}") from e

        if data is None:
            return []
        if not isinstance(data, list):
            raise ValueError(f"Expected a JSON list in {self.filename}")

        todos: list[dict[str, Any]] = []
        for item in data:
            if not isinstance(item, dict):
                continue
            if "id" not in item or "text" not in item or "done" not in item:
                continue
            try:
                todo_id = int(item["id"])
            except Exception:
                continue
            todos.append(
                {
                    "id": todo_id,
                    "text": str(item["text"]),
                    "done": bool(item["done"]),
                }
            )
        return todos

    def save(self, todos: list[dict[str, Any]]) -> None:
        """Persist todos to disk (atomically when possible)."""
        parent_dir = os.path.dirname(os.path.abspath(self.filename)) or "."
        fd, tmp_path = tempfile.mkstemp(prefix=".todos.", suffix=".json", dir=parent_dir)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(todos, f, ensure_ascii=False)
            os.replace(tmp_path, self.filename)
        finally:
            # If os.replace succeeded, tmp_path no longer exists; ignore errors.
            try:
                os.unlink(tmp_path)
            except FileNotFoundError:
                pass

    def next_id(self) -> int:
        """Return the next auto-incrementing todo ID."""
        todos = self.load()
        max_id = 0
        for t in todos:
            try:
                max_id = max(max_id, int(t.get("id", 0)))
            except Exception:
                continue
        return max_id + 1
