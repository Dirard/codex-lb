import { act, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithProviders } from "@/test/utils";
import { KeyReportsSchema, type KeyReport } from "./api";
import { KeyReportLimits } from "./key-report-limits";

const now = Date.parse("2026-10-02T00:00:00Z");
const later = (minutes: number) => new Date(now + minutes * 60_000).toISOString();
const limits: KeyReport["limits"] = [{ id: 1, limitType: "cost_usd", limitWindow: "weekly",
  currentValue: 2_500_000, maxValue: 10_000_000, modelFilter: null, resetAt: later(125) }];

function group(): NonNullable<KeyReport["group"]> {
  return { name: "Team A", keys: [], accountQuota: {
    accountCount: 6, purchasedCredits: 342682.95, creditsUnlimited: false, creditsKnownAccountCount: 6,
    windows: [{ window: "secondary", usedPercent: 98, accountCount: 5, nextResetAt: later(90) },
      { window: "monthly", usedPercent: 0, accountCount: 1, nextResetAt: later(3 * 24 * 60) }],
  } };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(now);
});
afterEach(() => vi.useRealTimers());

describe("compact key limit cards", () => {
  it("shows shared usage only without a personal cap while keeping credits and live countdowns", () => {
    const { rerender, unmount } = renderWithProviders(<KeyReportLimits limits={limits} group={group()} />);
    const personal = screen.getByRole("region", { name: "Personal limits" });
    expect(screen.queryByRole("region", { name: "Shared limits" })).not.toBeInTheDocument();
    const credits = screen.getByRole("region", { name: "Extra credits" });
    expect(within(personal).getByText("$2.50 / $10.00")).toBeInTheDocument();
    expect(within(personal).getByText("25%")).toBeInTheDocument();
    expect(within(personal).getByText("Reset in 2h 5m")).toHaveAttribute("datetime", later(125));
    expect(within(credits).getByText("342,682.95")).toBeInTheDocument();
    expect(screen.queryByText(/Known credits:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Combined usage weighted/)).not.toBeInTheDocument();
    act(() => vi.advanceTimersByTime(60_000));
    expect(within(personal).getByText("Reset in 2h 4m")).toBeInTheDocument();
    rerender(<KeyReportLimits limits={[]} group={group()} />);
    expect(personal).toHaveTextContent("Unlimited");
    const shared = screen.getByRole("region", { name: "Shared limits" });
    expect(within(shared).getByRole("progressbar", { name: "Weekly usage" })).toHaveAttribute("aria-valuenow", "98");
    expect(within(shared).queryByText("5-hour usage")).not.toBeInTheDocument();
    expect(within(shared).getByText("Next reset in 1h 29m")).toBeInTheDocument();
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not confuse an unlimited key with hidden or absent account quotas", () => {
    const { rerender } = renderWithProviders(<KeyReportLimits limits={[]} group={null} />);
    expect(screen.getByRole("region", { name: "Personal limits" })).toHaveTextContent("Unlimited");
    expect(screen.queryByRole("region", { name: "Shared limits" })).not.toBeInTheDocument();
    rerender(<KeyReportLimits limits={[]} group={{ ...group(), accountQuota: null }} />);
    expect(screen.queryByRole("region", { name: "Shared limits" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Extra credits" })).not.toBeInTheDocument();
  });

  it("keeps unknown, zero, partial and unlimited purchased balances distinct", () => {
    const data = group();
    data.accountQuota!.purchasedCredits = null;
    const { rerender } = renderWithProviders(<KeyReportLimits limits={[]} group={data} />);
    const credits = screen.getByRole("region", { name: "Extra credits" });
    expect(within(credits).getByText("—")).toBeInTheDocument();
    data.accountQuota!.purchasedCredits = 0;
    rerender(<KeyReportLimits limits={[]} group={data} />);
    expect(within(credits).getByText("0.00")).toBeInTheDocument();
    data.accountQuota!.purchasedCredits = 25.5;
    data.accountQuota!.creditsKnownAccountCount = 2;
    rerender(<KeyReportLimits limits={[]} group={data} />);
    expect(within(credits).getByText("≥ 25.50")).toBeInTheDocument();
    expect(within(credits).getByText("Partial balance")).toHaveAttribute("title", "Known credits: 2 of 6 subscription accounts");
    data.accountQuota!.creditsUnlimited = true;
    rerender(<KeyReportLimits limits={[]} group={data} />);
    expect(within(credits).getByText("Unlimited")).toBeInTheDocument();
    expect(within(credits).queryByText("Partial balance")).not.toBeInTheDocument();
  });

  it("does not invent a reset time or reset consumption locally", () => {
    const data = group();
    data.accountQuota!.windows = [{ window: "secondary", usedPercent: 98, accountCount: 5 }];
    expect(KeyReportsSchema.shape.group.parse(data)?.accountQuota?.windows[0].nextResetAt).toBeUndefined();
    const { rerender } = renderWithProviders(<KeyReportLimits limits={[{ ...limits[0], currentValue: 12_000_000, resetAt: later(-1) }]} group={data} />);
    expect(screen.getByText("Awaiting reset")).toBeInTheDocument();
    expect(screen.getByText("120%")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Personal limits" })).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    rerender(<KeyReportLimits limits={[]} group={data} />);
    expect(screen.getByText("Reset time unavailable")).toBeInTheDocument();
  });
});
