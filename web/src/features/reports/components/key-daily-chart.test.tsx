import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createUsageMeasures } from "@/test/mocks/key-usage-report-factories";

import type { KeyReportDay, KeyReportSeries } from "../schemas";
import { buildDailyChartRows, DAILY_CHART_MARGIN } from "../daily-chart-rows";
import { KeyDailyChart } from "./key-daily-chart";

type CapturedChart = { data: Record<string, unknown>[]; margin: { left: number; right: number } };
type CapturedBar = { dataKey: string; stackId: string; fill: string };

let chart: CapturedChart | null = null;
let bars: CapturedBar[] = [];

vi.mock("@/components/lazy-recharts", () => ({
  ResponsiveContainer: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  BarChart: (props: CapturedChart & { children: ReactNode }) => {
    chart = props;
    return <div data-testid="bar-chart">{props.children}</div>;
  },
  Bar: (props: CapturedBar) => {
    bars.push(props);
    return null;
  },
  CartesianGrid: () => null,
  XAxis: () => null,
  YAxis: () => null,
  Tooltip: () => null,
}));

const SERIES: KeyReportSeries[] = [
  { id: "key_1", name: "Alpha", kind: "key" },
  { id: null, name: "", kind: "ungrouped" },
  { id: null, name: "other", kind: "other" },
];

function day(date: string, points: [number, number, number][]): KeyReportDay {
  return {
    date,
    total: createUsageMeasures({ requests: points.reduce((sum, [requests]) => sum + requests, 0) }),
    series: points.map(([requests, tokens, costUsd]) => ({ requests, tokens, costUsd })),
  };
}

const DAILY = [day("2026-10-01", [[1, 10, 0.5], [2, 20, 0.25], [3, 30, 0.1]]), day("2026-10-03", [[4, 40, 1], [0, 0, 0], [0, 0, 0]])];

describe("buildDailyChartRows", () => {
  it("fills every day of the period with zero rows and maps series by position", () => {
    const rows = buildDailyChartRows("2026-10-01", "2026-10-04", DAILY, SERIES.length, "cost");
    expect(rows.map((row) => row.date)).toEqual(["2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04"]);
    expect(rows[0]).toMatchObject({ label: "10-01", s0: 0.5, s1: 0.25, s2: 0.1 });
    expect(rows[1]).toMatchObject({ s0: 0, s1: 0, s2: 0 });
    expect(rows[2]).toMatchObject({ s0: 1, s1: 0, s2: 0 });
    expect(buildDailyChartRows("2026-10-01", "2026-10-01", DAILY, SERIES.length, "requests")[0]).toMatchObject({ s0: 1, s1: 2, s2: 3 });
    expect(buildDailyChartRows("2026-10-01", "2026-10-01", DAILY, SERIES.length, "tokens")[0]).toMatchObject({ s0: 10, s1: 20, s2: 30 });
  });

  it("plots the day's total when there are no series", () => {
    const rows = buildDailyChartRows("2026-10-01", "2026-10-02", [day("2026-10-01", [])], 0, "requests");
    expect(rows).toEqual([
      { date: "2026-10-01", label: "10-01", s0: 0 },
      { date: "2026-10-02", label: "10-02", s0: 0 },
    ]);
  });
});

describe("KeyDailyChart", () => {
  beforeEach(() => {
    chart = null;
    bars = [];
  });

  it("stacks one bar per series with symmetric padding, a neutral other and localized names", async () => {
    const user = userEvent.setup();
    render(<KeyDailyChart start="2026-10-01" end="2026-10-03" series={SERIES} daily={DAILY} empty={false} />);
    expect(screen.getByRole("heading", { name: "Daily usage" })).toBeInTheDocument();
    expect(chart?.margin.left).toBe(chart?.margin.right);
    expect(chart?.margin).toEqual(DAILY_CHART_MARGIN);
    expect(chart?.data).toHaveLength(3);
    expect(new Set(bars.map((bar) => bar.stackId))).toEqual(new Set(["daily"]));
    expect(bars.map((bar) => bar.dataKey)).toEqual(["s0", "s1", "s2"]);
    expect(bars[2].fill).toBe("#94a3b8");
    const legend = screen.getByRole("list", { name: "Series" });
    expect(legend).toHaveTextContent("Alpha");
    expect(legend).toHaveTextContent("Ungrouped keys");
    expect(legend).toHaveTextContent("Other");

    expect(screen.getByRole("button", { name: "Cost" })).toHaveAttribute("aria-pressed", "true");
    await user.click(screen.getByRole("button", { name: "Requests" }));
    expect(screen.getByRole("button", { name: "Requests" })).toHaveAttribute("aria-pressed", "true");
    expect(chart?.data[0]).toMatchObject({ s0: 1, s1: 2, s2: 3 });
    await user.click(screen.getByRole("button", { name: "Tokens" }));
    expect(chart?.data[0]).toMatchObject({ s0: 10, s1: 20, s2: 30 });
  });

  it("shows the empty state without usage", () => {
    render(<KeyDailyChart start="2026-10-01" end="2026-10-03" series={[]} daily={[]} empty />);
    expect(screen.getByText("No usage in this period")).toBeInTheDocument();
    expect(screen.queryByTestId("bar-chart")).not.toBeInTheDocument();
  });
});
