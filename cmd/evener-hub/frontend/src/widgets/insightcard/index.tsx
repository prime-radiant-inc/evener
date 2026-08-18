// Adapted from Beautiful UI's Insight Cards (beautifului.dev, MIT © 2026 Shane Levine) — see LICENSES/beautiful-ui.txt.
import { Chevron } from "../chevron";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import styles from "./insightcard.module.css";

export interface Insight {
  title: string;
  body: string;
  /** Optional trend data, oldest first. Fewer than 2 points can't draw a
   * line, so no chart renders for an absent, empty, or single-point series. */
  series?: number[];
}

export interface InsightCardProps {
  insights: Insight[];
  /** 0-indexed into `insights`. */
  page: number;
  onPageChange: (page: number) => void;
}

const BASE_CLASS = {
  card: requireClass(styles.card, "insightcard.module.css", "card"),
  header: requireClass(styles.header, "insightcard.module.css", "header"),
  nav: requireClass(styles.nav, "insightcard.module.css", "nav"),
  counter: requireClass(styles.counter, "insightcard.module.css", "counter"),
  title: requireClass(styles.title, "insightcard.module.css", "title"),
  body: requireClass(styles.body, "insightcard.module.css", "body"),
  chartWrap: requireClass(styles.chartWrap, "insightcard.module.css", "chartWrap"),
  chart: requireClass(styles.chart, "insightcard.module.css", "chart"),
  area: requireClass(styles.area, "insightcard.module.css", "area"),
  line: requireClass(styles.line, "insightcard.module.css", "line"),
  srOnly: requireClass(styles.srOnly, "insightcard.module.css", "srOnly"),
};

// Fixed-height coordinate system; width tracks the series' own point count
// (one unit per gap between points) so the viewBox itself is "scaled to the
// data" rather than to an arbitrary pixel box - see index.tsx's own note on
// vector-effect below for how that stays visually crisp regardless of size.
const CHART_HEIGHT = 100;

interface ChartGeometry {
  viewBoxWidth: number;
  linePath: string;
  areaPath: string;
  min: number;
  max: number;
}

function chartGeometryFor(series: number[]): ChartGeometry | null {
  if (series.length < 2) return null; // can't draw a line through fewer than 2 points

  const viewBoxWidth = series.length - 1;
  const min = Math.min(...series);
  const max = Math.max(...series);
  const range = max - min;

  const points = series.map((value, i) => {
    const x = i;
    // range===0 (a flat series) would divide by zero - draw it as a flat
    // mid-height line instead.
    const y = range === 0 ? CHART_HEIGHT / 2 : CHART_HEIGHT - ((value - min) / range) * CHART_HEIGHT;
    return { x, y };
  });

  const linePath = points.map((p, i) => `${i === 0 ? "M" : "L"}${p.x} ${p.y}`).join(" ");
  const areaPath = `${linePath} L${viewBoxWidth} ${CHART_HEIGHT} L0 ${CHART_HEIGHT} Z`;

  return { viewBoxWidth, linePath, areaPath, min, max };
}

/**
 * A card presenting one of several agent insights at a time, with prev/next
 * pagination (controlled - `page`/`onPageChange` are owned by the caller,
 * same shape as every other controlled widget in this library) and, when
 * the current insight carries trend data, a small inline sparkline. No
 * animation and no charting library: the sparkline is two plain SVG paths
 * (a stroke and a color-mix wash beneath it) whose viewBox tracks the
 * series' own point count, with `vector-effect="non-scaling-stroke"` on the
 * stroke so it stays a crisp hairline regardless of the box it's laid out
 * into - a literal SVG width in user units would fatten or thin with it
 * instead.
 */
export function InsightCard({ insights, page, onPageChange }: InsightCardProps) {
  const total = insights.length;
  const insight = insights[page];
  const geometry = insight?.series ? chartGeometryFor(insight.series) : null;

  return (
    <div className={BASE_CLASS.card}>
      <div className={BASE_CLASS.header}>
        <span className={BASE_CLASS.counter}>
          {total === 0 ? 0 : page + 1} of {total}
        </span>
        <span className={BASE_CLASS.nav}>
          <IconButton
            label="Previous insight"
            variant="quiet"
            size="xs"
            icon={<Chevron direction="left" />}
            disabled={page <= 0 || total === 0}
            onClick={() => onPageChange(page - 1)}
          />
          <IconButton
            label="Next insight"
            variant="quiet"
            size="xs"
            icon={<Chevron direction="right" />}
            disabled={total === 0 || page >= total - 1}
            onClick={() => onPageChange(page + 1)}
          />
        </span>
      </div>
      {insight && (
        <>
          <p data-testid="insightcard-title" className={BASE_CLASS.title}>
            {insight.title}
          </p>
          <p data-testid="insightcard-body" className={BASE_CLASS.body}>
            {insight.body}
          </p>
        </>
      )}
      {geometry && (
        <div className={BASE_CLASS.chartWrap}>
          <svg
            data-testid="insightcard-chart"
            className={BASE_CLASS.chart}
            viewBox={`0 0 ${geometry.viewBoxWidth} ${CHART_HEIGHT}`}
            preserveAspectRatio="none"
            aria-hidden="true"
          >
            <path className={BASE_CLASS.area} d={geometry.areaPath} />
            <path className={BASE_CLASS.line} d={geometry.linePath} vectorEffect="non-scaling-stroke" />
          </svg>
          <span className={BASE_CLASS.srOnly}>
            Trend ranging from {geometry.min} to {geometry.max}
          </span>
        </div>
      )}
    </div>
  );
}
