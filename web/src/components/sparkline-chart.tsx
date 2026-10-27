import { useId } from "react";

import { useReducedMotion } from "@/hooks/use-reduced-motion";

const VIEWBOX_WIDTH = 100;
const VERTICAL_MARGIN = 2;

export type SparklineChartProps = {
  data: { value: number }[];
  color: string;
  index: number;
  height?: number;
};

type Point = { x: number; y: number };

/**
 * Monotone cubic interpolation along x (Fritsch–Carlson, as d3's
 * `curveMonotoneX`): the curve never overshoots the data, so a sparkline of
 * non-negative counts never dips below its baseline.
 */
function monotonePath(points: Point[]): string {
  if (points.length === 0) {
    return "";
  }
  if (points.length === 1) {
    return `M ${points[0].x} ${points[0].y}`;
  }
  const n = points.length;
  const secants: number[] = [];
  for (let i = 0; i < n - 1; i += 1) {
    const dx = points[i + 1].x - points[i].x;
    secants.push(dx === 0 ? 0 : (points[i + 1].y - points[i].y) / dx);
  }
  const tangents: number[] = new Array(n);
  tangents[0] = secants[0];
  tangents[n - 1] = secants[n - 2];
  for (let i = 1; i < n - 1; i += 1) {
    const before = secants[i - 1];
    const after = secants[i];
    if (before * after <= 0) {
      tangents[i] = 0;
    } else {
      const dxBefore = points[i].x - points[i - 1].x;
      const dxAfter = points[i + 1].x - points[i].x;
      const total = dxBefore + dxAfter;
      // Weighted harmonic mean keeps the interpolant monotone.
      tangents[i] = (3 * total) / ((total + dxAfter) / before + (total + dxBefore) / after);
    }
  }
  const round = (value: number) => Number(value.toFixed(3));
  let path = `M ${round(points[0].x)} ${round(points[0].y)}`;
  for (let i = 0; i < n - 1; i += 1) {
    const start = points[i];
    const end = points[i + 1];
    const third = (end.x - start.x) / 3;
    path += ` C ${round(start.x + third)} ${round(start.y + tangents[i] * third)}, ${round(end.x - third)} ${round(end.y - tangents[i + 1] * third)}, ${round(end.x)} ${round(end.y)}`;
  }
  return path;
}

/** Scales values into the viewBox with a zero-based domain, like the previous area chart. */
function sparklinePoints(data: { value: number }[], height: number): Point[] {
  const values = data.map((entry) => (Number.isFinite(entry.value) ? entry.value : 0));
  const top = Math.max(0, ...values);
  const bottom = Math.min(0, ...values);
  const span = top - bottom;
  const usable = height - VERTICAL_MARGIN * 2;
  const step = values.length > 1 ? VIEWBOX_WIDTH / (values.length - 1) : 0;
  return values.map((value, index) => ({
    x: step * index,
    y: span === 0 ? height - VERTICAL_MARGIN : VERTICAL_MARGIN + ((top - value) / span) * usable,
  }));
}

export function SparklineChart({ data, color, index, height = 40 }: SparklineChartProps) {
  const reducedMotion = useReducedMotion();
  const gradientId = `sparkline-fill-${useId().replace(/:/g, "")}`;
  const points = sparklinePoints(data, height);
  const line = monotonePath(points);
  const baseline = height - VERTICAL_MARGIN;
  const area = points.length > 1
    ? `${line} L ${points[points.length - 1].x} ${baseline} L ${points[0].x} ${baseline} Z`
    : "";

  return (
    <svg
      aria-hidden="true"
      data-testid="sparkline"
      className={reducedMotion ? "block" : "block animate-in fade-in duration-500"}
      style={reducedMotion ? undefined : { animationDelay: `${index * 100}ms`, animationFillMode: "both" }}
      width="100%"
      height={height}
      viewBox={`0 0 ${VIEWBOX_WIDTH} ${height}`}
      preserveAspectRatio="none"
    >
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity={0.3} />
          <stop offset="100%" stopColor={color} stopOpacity={0.05} />
        </linearGradient>
      </defs>
      {area ? <path d={area} fill={`url(#${gradientId})`} stroke="none" /> : null}
      <path d={line} fill="none" stroke={color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
