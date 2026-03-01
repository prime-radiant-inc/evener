"""Eval dashboard server."""

from fastapi import FastAPI

app = FastAPI(title="Serf Eval Dashboard")


@app.get("/health")
def health():
    return {"status": "ok"}
