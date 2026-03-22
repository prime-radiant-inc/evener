#!/usr/bin/env python3
from __future__ import annotations

import argparse
import sys
from typing import Any

from storage import TodoStore


def _format_todo(todo: dict[str, Any]) -> str:
    mark = "x" if todo.get("done") else " "
    return f"[{todo.get('id')}] [{mark}] {todo.get('text')}"


def cmd_add(args: argparse.Namespace) -> int:
    store = TodoStore()
    todos = store.load()
    todo_id = store.next_id()
    # Allow multi-word text without requiring quotes.
    text = " ".join(args.text)
    todos.append({"id": todo_id, "text": text, "done": False})
    store.save(todos)
    print(f"Added: {text} (id={todo_id})")
    return 0


def cmd_list(args: argparse.Namespace) -> int:
    store = TodoStore()
    todos = store.load()
    if not todos:
        print("No todos.")
        return 0
    for todo in sorted(todos, key=lambda t: int(t.get("id", 0))):
        print(_format_todo(todo))
    return 0


def _find_by_id(todos: list[dict[str, Any]], todo_id: int) -> tuple[int, dict[str, Any]] | None:
    for idx, t in enumerate(todos):
        try:
            if int(t.get("id")) == todo_id:
                return idx, t
        except Exception:
            continue
    return None


def cmd_done(args: argparse.Namespace) -> int:
    store = TodoStore()
    todos = store.load()
    found = _find_by_id(todos, args.id)
    if found is None:
        print(f"Todo {args.id} not found.", file=sys.stderr)
        return 1
    idx, todo = found
    if not todo.get("done"):
        todo = {**todo, "done": True}
        todos[idx] = todo
        store.save(todos)
    print(f"Completed: {todo.get('text')}")
    return 0


def cmd_delete(args: argparse.Namespace) -> int:
    store = TodoStore()
    todos = store.load()
    found = _find_by_id(todos, args.id)
    if found is None:
        print(f"Todo {args.id} not found.", file=sys.stderr)
        return 1
    idx, todo = found
    del todos[idx]
    store.save(todos)
    print(f"Deleted: {todo.get('text')}")
    return 0


def cmd_search(args: argparse.Namespace) -> int:
    store = TodoStore()
    todos = store.load()
    q = " ".join(args.query).casefold()
    matches = [t for t in todos if q in str(t.get("text", "")).casefold()]
    if not matches:
        print("No matches.")
        return 0
    for todo in sorted(matches, key=lambda t: int(t.get("id", 0))):
        print(_format_todo(todo))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="todo", description="Simple todo list CLI")
    sub = parser.add_subparsers(dest="command", required=True)

    p_add = sub.add_parser("add", help="add a new todo")
    p_add.add_argument("text", nargs="+", help="todo text")
    p_add.set_defaults(func=cmd_add)

    p_list = sub.add_parser("list", help="list todos")
    p_list.set_defaults(func=cmd_list)

    p_done = sub.add_parser("done", help="mark todo as done")
    p_done.add_argument("id", type=int, help="todo id")
    p_done.set_defaults(func=cmd_done)

    p_delete = sub.add_parser("delete", help="delete a todo")
    p_delete.add_argument("id", type=int, help="todo id")
    p_delete.set_defaults(func=cmd_delete)

    p_search = sub.add_parser("search", help="search todos")
    p_search.add_argument("query", nargs="+", help="query string")
    p_search.set_defaults(func=cmd_search)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
