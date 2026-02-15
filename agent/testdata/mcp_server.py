#!/usr/bin/env python3
"""Minimal MCP stdio server for integration testing.

Speaks JSON-RPC over stdin/stdout per MCP spec.
Provides one tool: "echo" that returns its input.
"""
import json
import sys


def respond(id_, result):
    msg = {"jsonrpc": "2.0", "id": id_, "result": result}
    data = json.dumps(msg)
    sys.stdout.write(data + "\n")
    sys.stdout.flush()


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue

        method = req.get("method", "")
        id_ = req.get("id")

        if method == "initialize":
            respond(id_, {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "test-mcp", "version": "1.0.0"},
            })
        elif method == "notifications/initialized":
            pass  # notification, no response
        elif method == "tools/list":
            respond(id_, {
                "tools": [
                    {
                        "name": "echo",
                        "description": "Echoes back the input message",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "message": {
                                    "type": "string",
                                    "description": "Message to echo",
                                }
                            },
                            "required": ["message"],
                        },
                    }
                ]
            })
        elif method == "tools/call":
            args = req.get("params", {}).get("arguments", {})
            message = args.get("message", "")
            respond(id_, {
                "content": [{"type": "text", "text": f"echo: {message}"}]
            })
        else:
            # Unknown method — respond with empty result
            if id_ is not None:
                respond(id_, {})


if __name__ == "__main__":
    main()
