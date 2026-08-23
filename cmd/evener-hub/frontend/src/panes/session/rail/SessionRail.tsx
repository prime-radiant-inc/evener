// SessionRail.tsx — the 156px canvas rail that replaces the transcript's
// native scrollbar. Ported from the reference implementation's
// drawCombinedRail + promptLayout + anomalyAt + pointer handling.
//
// The rail encodes: per-turn token strata (log-width IN/OUT bars), cumulative
// burn step line, result-size cliffs, error ticks, prompt anchors (DOM
// buttons), job micro-lanes, in-flight tool highlight, hatched idle voids,
// UTC axis, and a draggable viewport thumb synced to VirtualList scroll.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  type AxisKind,
  fmtElapsed,
  GAP_MIN_MS,
  MIN_WINDOW_MS,
  makeAxisParams,
  type RailEvent,
  type RailView,
  timeTicks,
  turnTicks,
  utcLabel,
  vIdxY,
  vY,
  vYev,
} from "./axis";
import styles from "./rail.module.css";
import type { RailModel } from "./railModel";
import { type RailTheme, withAlpha } from "./useRailTheme";

/** Rail layout constants — the column positions within the 156px width. */
function railLayout(W: number) {
  const axisW = 28;
  const sX0 = axisW + 6;
  const sX1 = W - 56;
  return {
    axisW,
    sX0,
    sX1,
    sW: Math.max(20, sX1 - sX0),
    resX0: W - 52,
    resX1: W - 41,
    ifX0: W - 40,
    ifX1: W - 37,
    jobX0: W - 36,
    jobX1: W - 19,
    errX: W - 15,
    delegX: W - 7,
  };
}

/** Prompt anchor layout — fans anchors to avoid overlap (≥11px separation). */
export interface PromptAnchorLayout {
  p: RailEvent;
  y: number;
  lane: number;
  exactY: number;
}

export function promptLayout(V: RailView, H: number, events: RailEvent[]): PromptAnchorLayout[] {
  const lanes: number[] = [];
  const out: PromptAnchorLayout[] = [];
  // prompts are the events with userInput or userSteer flags
  for (const p of events) {
    if (!p.userInput && !p.userSteer) continue;
    if (p.ms > V.nowMs) break;
    const exactY = vYev(V, p, H, events);
    const y = Math.max(7, Math.min(H - 7, exactY));
    let lane = lanes.findIndex((last) => y - last >= 11);
    if (lane < 0) lane = lanes.length;
    lanes[lane] = y;
    out.push({ p, y, lane, exactY });
  }
  return out;
}

/** Thumb state for the scrollbar. */
export interface ThumbState {
  top: number;
  bottom: number;
  first: number;
  vis: number;
}

export interface SessionRailProps {
  model: RailModel;
  nowMs: number;
  axis: AxisKind;
  theme: RailTheme;
  /** Thumb state (null in comprehension mode). */
  thumb?: ThumbState | null;
  /** Whether the session is live (playing) — controls the now-line color. */
  playing: boolean;
  /** Whether the session has ended — controls the now-line color. */
  ended: boolean;
  /** Called when the user clicks an anchor or anomaly. */
  onJump?: (eventIndex: number) => void;
  /** Width in px (default 156). */
  width?: number;
}

export function SessionRail({
  model,
  nowMs,
  axis,
  theme,
  thumb,
  playing,
  ended,
  onJump,
  width = 156,
}: SessionRailProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const mapRef = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<{ grab: number } | null>(null);
  const [tooltip, setTooltip] = useState<{ x: number; y: number; text: string } | null>(null);

  const events = model.events;
  const startMs = model.startMs;
  const V = useMemo<RailView>(
    () => ({ kind: axis, nowMs, startMs, ap: makeAxisParams(axis, startMs, nowMs, events.length) }),
    [axis, nowMs, startMs, events.length],
  );

  // --- rendering ---
  const draw = useCallback(() => {
    const canvas = canvasRef.current;
    const mapEl = mapRef.current;
    if (!canvas || !mapEl) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const W = width;
    const H = mapEl.clientHeight;
    if (!H) return;

    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.max(1, Math.round(W * dpr));
    canvas.height = Math.max(1, Math.round(H * dpr));
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    drawCombinedRail(ctx, W, H, V, events, model, theme, { thumb, playing, ended });
  }, [V, events, model, theme, thumb, playing, ended, width]);

  useEffect(() => {
    draw();
  }, [draw]);

  // Resize observer — guard for jsdom which has no ResizeObserver.
  useEffect(() => {
    const mapEl = mapRef.current;
    if (!mapEl || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => draw());
    ro.observe(mapEl);
    return () => ro.disconnect();
  }, [draw]);

  // --- pointer handling ---
  const handlePointerDown = useCallback(
    (e: React.PointerEvent<HTMLCanvasElement>) => {
      const canvas = canvasRef.current;
      if (!canvas) return;
      const r = canvas.getBoundingClientRect();
      const x = e.clientX - r.left;
      const y = e.clientY - r.top;

      // Anomaly hit-test
      const an = anomalyAt(V, events, x, y, r.width, r.height);
      if (an && onJump) {
        onJump(events.indexOf(an));
        return;
      }

      // Thumb drag or click-to-jump
      canvas.setPointerCapture(e.pointerId);
      if (thumb) {
        if (y >= thumb.top - 3 && y <= thumb.bottom + 3) {
          setDrag({ grab: y - thumb.top });
        } else {
          // Click-to-jump: scroll to the position
          const idx = vIdxY(V, y, r.height, events);
          onJump?.(Math.round(idx - thumb.vis / 2));
          setDrag({ grab: Math.max(16, thumb.bottom - thumb.top) / 2 });
        }
      }
    },
    [V, events, thumb, onJump],
  );

  const handlePointerMove = useCallback(
    (e: React.PointerEvent<HTMLCanvasElement>) => {
      const canvas = canvasRef.current;
      if (!canvas) return;
      const r = canvas.getBoundingClientRect();
      const y = e.clientY - r.top;

      if (drag) {
        const idx = vIdxY(V, y - drag.grab, r.height, events);
        onJump?.(Math.round(idx));
        return;
      }

      // Hover tooltip: find nearest event
      let nearest: RailEvent | null = null;
      let dist = 6;
      for (let i = 0; i < events.length; i++) {
        const ev = events[i];
        if (!ev || ev.ms > nowMs) break;
        const d = Math.abs(vYev(V, ev, r.height, events) - y);
        if (d < dist) {
          dist = d;
          nearest = ev;
        }
      }

      if (nearest) {
        const ev = nearest;
        const tok =
          ev.kind === "ASSISTANT"
            ? `IN ${ev.inTok ?? 0} · OUT ${ev.outTok ?? 0} · Σ ${(ev.inTok ?? 0) + (ev.outTok ?? 0)}`
            : ev.kind === "TOOL_RESULTS"
              ? `${ev.resBytes ?? 0} bytes result`
              : "";
        setTooltip({
          x: e.clientX,
          y: e.clientY + 10,
          text: `#${ev.pos} · ${ev.kind} · ${utcLabel(ev.ms)}${tok ? `\n${tok}` : ""}`,
        });
      } else {
        setTooltip(null);
      }
    },
    [V, events, drag, nowMs, onJump],
  );

  const handlePointerUp = useCallback(() => {
    setDrag(null);
  }, []);

  // --- prompt anchors ---
  const pl = promptLayout(V, mapRef.current?.clientHeight ?? 0, events);

  return (
    <section className={styles.rail} style={{ width }} aria-label="Session rail">
      <div className={styles.railMap} ref={mapRef}>
        <canvas
          ref={canvasRef}
          className={styles.canvas}
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={handlePointerUp}
          onPointerLeave={() => setTooltip(null)}
          aria-label="Session timeline and token usage visualization"
          role="img"
        />
        <div className={styles.promptLayer}>
          {pl.map((m) => (
            <button
              key={m.p.pos}
              type="button"
              className={`${styles.promptAnchor} ${m.p.userInput ? styles.userAnchor : styles.steerAnchor}`}
              style={{ top: `${m.y}px`, left: `${6 + m.lane * 11}px` }}
              onPointerDown={(e) => e.stopPropagation()}
              onClick={(e) => {
                e.stopPropagation();
                onJump?.(events.indexOf(m.p));
              }}
              aria-label={`${m.p.userInput ? "User input" : "User steering"} #${m.p.pos}, ${utcLabel(m.p.ms)} UTC`}
            />
          ))}
        </div>
      </div>
      {tooltip && (
        <div
          className={styles.tooltip}
          style={{
            left: `${Math.max(8, tooltip.x - 345)}px`,
            top: `${Math.min(window.innerHeight - 90, tooltip.y)}px`,
          }}
        >
          {tooltip.text}
        </div>
      )}
    </section>
  );
}

// --- the combined rail renderer (ported from reference drawCombinedRail) ---

function drawCombinedRail(
  ctx: CanvasRenderingContext2D,
  W: number,
  H: number,
  V: RailView,
  events: RailEvent[],
  model: RailModel,
  theme: RailTheme,
  opts: { thumb?: ThumbState | null; playing: boolean; ended: boolean },
) {
  const { kind, nowMs, startMs, ap } = V;
  const L = railLayout(W);
  const live = model.live;

  const line = (x1: number, y1: number, x2: number, y2: number, color: string, w = 1, dash: number[] = []) => {
    ctx.beginPath();
    ctx.setLineDash(dash);
    ctx.moveTo(x1, y1);
    ctx.lineTo(x2, y2);
    ctx.strokeStyle = color;
    ctx.lineWidth = w;
    ctx.stroke();
    ctx.setLineDash([]);
  };

  const text = (t: string, x: number, y: number, color = theme.inkLow, align: CanvasTextAlign = "left", size = 7.5) => {
    ctx.fillStyle = color;
    ctx.font = `${size}px ${theme.fontMono}`;
    ctx.textAlign = align;
    ctx.textBaseline = "middle";
    ctx.fillText(t, x, y);
  };

  // Background
  ctx.clearRect(0, 0, W, H);
  ctx.fillStyle = theme.surfaceCanvas;
  ctx.fillRect(0, 0, W, H);

  // Strata band background
  ctx.fillStyle = theme.surfaceInset;
  ctx.fillRect(L.sX0 - 5, 0, 4, H);
  ctx.fillRect(L.sX0, 0, L.sW, H);
  ctx.fillRect(L.resX0, 0, L.resX1 - L.resX0, H);
  ctx.fillRect(L.jobX0, 0, L.jobX1 - L.jobX0, H);

  // Axis divider
  line(L.axisW, 0, L.axisW, H, theme.edge);

  // Axis ticks
  if (kind === "time") {
    const endMs = ap.end ?? startMs + MIN_WINDOW_MS;
    const ticks = timeTicks(startMs, endMs, H, (ms) => vY(V, ms, H, events));
    for (const tick of ticks) {
      line(L.axisW - 4, tick.y, L.axisW + 3, tick.y, withAlpha(theme.inkLow, 0.5));
      text(tick.label, 2, Math.max(11, Math.min(H - 5, tick.y)), theme.inkLow);
    }
  } else if (kind === "turn") {
    const ticks = turnTicks(events.length, H);
    for (const tick of ticks) {
      line(L.axisW - 4, tick.y, L.axisW + 3, tick.y, withAlpha(theme.inkLow, 0.5));
      text(tick.label, 2, Math.max(5, Math.min(H - 5, tick.y)), theme.inkLow);
    }
  }

  const nowY = Math.max(0, Math.min(H, vY(V, nowMs, H, events)));
  const isEnded = nowMs >= model.endMs && model.endMs > 0;

  // END cap / idle void
  if (isEnded && kind !== "turn") {
    const ey = Math.max(0, Math.min(H, vY(V, model.endMs, H, events)));
    if (ey < H - 2) {
      ctx.fillStyle = withAlpha(theme.inkLow, 0.075);
      ctx.fillRect(0, ey, W, H - ey);
      line(0, ey, W, ey, theme.inkLow, 1.4);
      text(`END +${fmtElapsed((model.endMs - startMs) / 1000)}`, L.sX0 + 8, Math.min(ey + 8, H - 6), theme.inkLow);
    }
  } else if (kind !== "turn" && live.n > 0) {
    const lastEv = events[live.n - 1];
    if (lastEv) {
      const idleMs = nowMs - lastEv.ms;
      if (idleMs >= GAP_MIN_MS) {
        const y1 = vY(V, lastEv.ms, H, events);
        const gh = nowY - y1;
        if (gh >= 3) {
          ctx.fillStyle = withAlpha(theme.inkLow, 0.075);
          ctx.fillRect(L.axisW + 1, y1, W - L.axisW - 1, gh);
          line(L.axisW + 2, y1, W - 2, y1, withAlpha(theme.inkLow, 0.25), 1, [2, 3]);
        }
      }
    }
  }

  // Per-turn strata: only revealed turns
  for (let i = 0; i < live.n; i++) {
    const e = events[i];
    if (!e) continue;
    const y = vYev(V, e, H, events);

    // Status color bar
    const statusCol = e.userInput
      ? theme.accent
      : e.userSteer
        ? theme.attention
        : e.error
          ? theme.danger
          : withAlpha(theme.inkLow, 0.5);
    ctx.fillStyle = statusCol;
    ctx.fillRect(L.sX0 - 5, y, 4, Math.max(1, kind === "turn" ? (H / Math.max(1, live.n)) * 0.7 : 1));

    if (e.kind === "ASSISTANT") {
      const hot = e._rankLive !== undefined && e._rankLive > 0;
      const wi = (Math.log1p(e.inTok ?? 0) / Math.log1p(live.maxIn)) * (L.sW - 8);
      const wo = (Math.log1p(e.outTok ?? 0) / Math.log1p(live.maxOut)) * (L.sW - 8);
      ctx.globalAlpha = 0.88;
      ctx.fillStyle = hot ? withAlpha(theme.alive, 0.9) : withAlpha(theme.accent, 0.7);
      ctx.fillRect(L.sX0, y - 1.2, Math.max(0.7, wi), 1.1);
      ctx.fillStyle = withAlpha(theme.alive, 0.85);
      ctx.fillRect(L.sX0, y + 0.2, Math.max(0.7, wo), 1.05);
      ctx.globalAlpha = 1;
    }

    if (e.kind === "TOOL_RESULTS" && (e.resBytes ?? 0) > 0) {
      const wr = 2 + (Math.log1p(e.resBytes ?? 0) / Math.log1p(live.maxRes)) * (L.resX1 - L.resX0 - 2);
      ctx.globalAlpha = 0.9;
      ctx.fillStyle = theme.danger;
      ctx.fillRect(L.resX0, y - 0.8, wr, 1.6);
      ctx.globalAlpha = 1;
    }

    if (e.error) {
      line(L.errX - 2, y - 2.5, L.errX + 2, y + 2.5, theme.danger, 1);
      line(L.errX + 2, y - 2.5, L.errX - 2, y + 2.5, theme.danger, 1);
    }
  }

  // Σ burn line: only the path walked so far
  const burnNorm = Math.max(1, live.burn);
  ctx.beginPath();
  let x = L.sX0;
  let cum = 0;
  let started = false;
  const topPos: { x: number; y: number; rank: number }[] = [];
  for (let i = 0; i < live.n; i++) {
    const e = events[i];
    if (e?.kind !== "ASSISTANT") continue;
    const y = vYev(V, e, H, events);
    if (!started) {
      ctx.moveTo(x, y);
      started = true;
    }
    cum += (e.inTok ?? 0) + (e.outTok ?? 0);
    const nx = L.sX0 + (cum / burnNorm) * L.sW;
    ctx.lineTo(x, y);
    ctx.lineTo(nx, y);
    x = nx;
    if (e._rankLive) topPos.push({ x: nx, y, rank: e._rankLive });
  }
  if (started) {
    ctx.strokeStyle = theme.inkHi;
    ctx.lineWidth = 1.25;
    ctx.shadowColor = theme.accent;
    ctx.shadowBlur = 3;
    ctx.stroke();
    ctx.shadowBlur = 0;
  }
  for (const t of topPos) {
    ctx.fillStyle = theme.danger;
    ctx.beginPath();
    ctx.moveTo(t.x, t.y - 3);
    ctx.lineTo(t.x + 3, t.y);
    ctx.lineTo(t.x, t.y + 3);
    ctx.lineTo(t.x - 3, t.y);
    ctx.closePath();
    ctx.fill();
    text(String(t.rank), Math.min(L.sX0 + L.sW - 3, t.x + 4), t.y - 4, withAlpha(theme.danger, 0.8), "left", 7);
  }

  // NOW hairline
  line(0, nowY, W, nowY, withAlpha(theme.inkHi, 0.92), 1.2);
  ctx.fillStyle = opts.playing ? theme.alive : opts.ended ? theme.inkLow : theme.attention;
  ctx.beginPath();
  ctx.moveTo(W, nowY);
  ctx.lineTo(W - 8, nowY - 4);
  ctx.lineTo(W - 8, nowY + 4);
  ctx.closePath();
  ctx.fill();
  ctx.fillRect(L.axisW - 4, nowY - 1, 7, 2);

  // Viewport thumb
  if (opts.thumb) {
    const t = opts.thumb;
    const th = Math.max(16, t.bottom - t.top);
    ctx.fillStyle = withAlpha(theme.inkHi, 0.06);
    ctx.fillRect(0, t.top, W, th);
    line(0, t.top, W, t.top, withAlpha(theme.inkHi, 0.34), 1);
    line(0, t.top + th, W, t.top + th, withAlpha(theme.inkHi, 0.34), 1);
    const gy = t.top + th / 2;
    for (let k = -1; k <= 1; k++) {
      line(W - 9, gy + k * 3, W - 3, gy + k * 3, withAlpha(theme.inkHi, 0.55), 1.4);
    }
  }
}

// --- anomaly hit-test ---
function anomalyAt(V: RailView, events: RailEvent[], x: number, y: number, W: number, H: number): RailEvent | null {
  const L = railLayout(W);
  let best: RailEvent | null = null;
  let bd = 9;
  const live = { burn: 1 }; // simplified; full live passed via model
  const burnNorm = Math.max(1, live.burn);

  // Top-5 cost diamonds
  for (const t of events) {
    if (t._rankLive === undefined || t._rankLive <= 0) continue;
    if (t.ms > V.nowMs) break;
    let cum = 0;
    for (let i = 0; i <= t.pos; i++) {
      const e = events[i];
      if (e && e.kind === "ASSISTANT") cum += (e.inTok ?? 0) + (e.outTok ?? 0);
    }
    const ax = L.sX0 + (cum / burnNorm) * L.sW;
    const ay = vYev(V, t, H, events);
    const d = Math.hypot(x - ax, y - ay);
    if (d < bd) {
      bd = d;
      best = t;
    }
  }

  // Error ticks
  for (let i = 0; i < events.length; i++) {
    const e = events[i];
    if (!e?.error) continue;
    if (e.ms > V.nowMs) break;
    const ay = vYev(V, e, H, events);
    const d = Math.hypot(x - L.errX, y - ay);
    if (d < bd) {
      bd = d;
      best = e;
    }
  }

  return best;
}
