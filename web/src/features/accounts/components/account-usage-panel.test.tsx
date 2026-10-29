import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AccountUsagePanel } from "@/features/accounts/components/account-usage-panel";
import { useDateDisplayFormatStore } from "@/hooks/use-date-format";
import { createAccountSummary, createAccountTrends } from "@/test/mocks/factories";
import { formatDateTimeInline } from "@/utils/formatters";

describe("AccountUsagePanel", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00.000Z"));
    useDateDisplayFormatStore.setState({ dateDisplayFormat: "iso8601" });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows '--' for missing quota percent instead of 0%", () => {
    const account = createAccountSummary({
      usage: {
        primaryRemainingPercent: null,
        secondaryRemainingPercent: 67,
      },
      windowMinutesPrimary: 300,
      windowMinutesSecondary: 10_080,
    });

    render(<AccountUsagePanel account={account} trends={null} />);

    expect(screen.getByText("5h remaining")).toBeInTheDocument();
    expect(screen.getByText("--")).toBeInTheDocument();
  });

  it("hides the 5h row and trend legend for weekly-only accounts", () => {
    const account = createAccountSummary({
      planType: "pro",
      usage: {
        primaryRemainingPercent: null,
        secondaryRemainingPercent: 76,
      },
      windowMinutesPrimary: null,
      windowMinutesSecondary: 10_080,
    });

    render(<AccountUsagePanel account={account} trends={createAccountTrends(account.accountId, { primary: [] })} />);

    expect(screen.queryByText("5h remaining")).not.toBeInTheDocument();
    expect(screen.queryByText("5h")).not.toBeInTheDocument();
    expect(screen.getByText("Weekly remaining")).toBeInTheDocument();
  });

  it("shows only Monthly for monthly-only free accounts", () => {
    const account = createAccountSummary({
      planType: "free",
      usage: {
        primaryRemainingPercent: null,
        secondaryRemainingPercent: null,
        monthlyRemainingPercent: 95,
      },
      windowMinutesPrimary: null,
      windowMinutesSecondary: null,
      windowMinutesMonthly: 43_200,
      resetAtPrimary: null,
      resetAtSecondary: null,
      resetAtMonthly: "2026-01-31T00:00:00.000Z",
    });

    render(<AccountUsagePanel account={account} trends={null} />);

    expect(screen.getByText("Monthly remaining")).toBeInTheDocument();
    expect(screen.queryByText("5h remaining")).not.toBeInTheDocument();
    expect(screen.queryByText("Weekly remaining")).not.toBeInTheDocument();
  });

  it("renders mapped label for the known gated additional quota limit", () => {
    const account = createAccountSummary({
      additionalQuotas: [
        {
          limitName: "codex_spark",
          meteredFeature: "codex_bengalfox",
          routingPolicy: "inherit",
          primaryWindow: {
            usedPercent: 35,
            resetAt: Math.floor(new Date("2026-01-07T13:00:00.000Z").getTime() / 1000),
            windowMinutes: 300,
          },
          secondaryWindow: null,
        },
      ],
    });

    render(<AccountUsagePanel account={account} trends={null} />);

    expect(screen.getByText("Additional Quotas")).toBeInTheDocument();
    expect(screen.getByText("GPT-5.3-Codex-Spark")).toBeInTheDocument();
    expect(screen.getByText(/35% used/)).toBeInTheDocument();
    expect(screen.getByText("Resets in 6d 13h")).toBeInTheDocument();
  });

  it("labels the last 7 days and the lifetime usage (R44)", () => {
    const account = createAccountSummary({
      requestUsage: {
        requestCount: 7,
        totalTokens: 51_480,
        cachedInputTokens: 41_470,
        totalCostUsd: 0.13,
      },
      usage7d: { requests: 3, totalTokens: 12_000, costUsd: 0.05 },
    });

    render(<AccountUsagePanel account={account} trends={null} />);

    const week = screen.getByTestId("usage-last-7-days");
    expect(week).toHaveTextContent("Last 7 days");
    expect(week).toHaveTextContent("12K");
    expect(week).toHaveTextContent("$0.05");
    const lifetime = screen.getByTestId("usage-lifetime");
    expect(lifetime).toHaveTextContent("Lifetime");
    expect(lifetime).toHaveTextContent("51.48K");
    expect(lifetime).toHaveTextContent("$0.13");
    expect(screen.queryByText("Request logs total")).not.toBeInTheDocument();
  });

  it("shows the snapshot reset credits with the time they were observed", () => {
    const account = createAccountSummary({ availableResetCredits: 3, resetCreditsObservedAt: "2026-01-01T00:00:00.000Z" });
    const clock = new Date("2026-01-01T00:00:00.000Z");
    const time = `${String(clock.getHours()).padStart(2, "0")}:${String(clock.getMinutes()).padStart(2, "0")}`;

    render(<AccountUsagePanel account={account} trends={null} onCheckNow={vi.fn()} />);

    expect(screen.getByText("Usage resets")).toBeInTheDocument();
    expect(screen.getByTestId("reset-credits-value")).toHaveTextContent(`3 available as of ${time}`);
  });

  it("prefers a Check now result for the same account and asks for Check now otherwise", () => {
    const onCheckNow = vi.fn();
    const account = createAccountSummary({ accountId: "acc_check", availableResetCredits: null });
    const view = render(<AccountUsagePanel account={account} trends={null} onCheckNow={onCheckNow} />);

    expect(screen.getByTestId("reset-credits-value")).toHaveTextContent("Not checked yet");
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    expect(onCheckNow).toHaveBeenCalledWith("acc_check");

    view.rerender(
      <AccountUsagePanel
        account={account}
        trends={null}
        onCheckNow={onCheckNow}
        checkedResetCredits={{ accountId: "acc_check", availableCount: 5, checkedAt: "2026-01-01T00:00:00.000Z" }}
      />,
    );
    expect(screen.getByTestId("reset-credits-value")).toHaveTextContent(/^5 available as of \d\d:\d\d$/);

    view.rerender(
      <AccountUsagePanel
        account={account}
        trends={null}
        onCheckNow={onCheckNow}
        checkedResetCredits={{ accountId: "acc_other", availableCount: 5, checkedAt: "2026-01-01T00:00:00.000Z" }}
      />,
    );
    expect(screen.getByTestId("reset-credits-value")).toHaveTextContent("Not checked yet");
  });

  it("shows the nearest reset-credit expiry when provided", () => {
    const expiresAt = "2026-01-01T02:34:56.000Z";
    const isoExpiry = formatDateTimeInline(expiresAt, "iso8601");
    const account = createAccountSummary({
      resetCreditNearestExpiresAt: expiresAt,
      availableResetCredits: 3,
    });

    render(<AccountUsagePanel account={account} trends={null} />);

    expect(
      screen.getByText((_, element) => {
        const text = element?.textContent ?? "";
        return text.startsWith("Expires ") && text.includes("(2h)") && !text.includes("available");
      }),
    ).toBeInTheDocument();
    const row = screen.getByText("Usage resets").closest("div");
    const rowText = row?.textContent ?? "";
    expect(rowText.indexOf("Expires ")).toBeGreaterThan(-1);
    expect(rowText.indexOf("Expires ")).toBeLessThan(rowText.indexOf("3 available"));
    expect(rowText).toContain(isoExpiry);

    act(() => {
      useDateDisplayFormatStore.setState({ dateDisplayFormat: "default" });
    });

    expect(row?.textContent).toContain(formatDateTimeInline(expiresAt, "default"));
    expect(row?.textContent).not.toContain(isoExpiry);
  });

  it("renders a usage reset action when credits are available", () => {
    const account = createAccountSummary({ accountId: "acc_reset", availableResetCredits: 1 });
    const onReset = vi.fn();

    const view = render(<AccountUsagePanel account={account} trends={null} onReset={onReset} />);

    fireEvent.click(screen.getByRole("button", { name: "Reset usage" }));

    expect(onReset).toHaveBeenCalledWith("acc_reset");
    expect(onReset).toHaveBeenCalledTimes(1);

    view.rerender(<AccountUsagePanel account={{ ...account, availableResetCredits: 0 }} trends={null} onReset={onReset} />);
    expect(screen.getByRole("button", { name: "Reset usage" })).toBeDisabled();
  });

  it("shows the weekly plan legend when scheduled trend data exists", () => {
    const account = createAccountSummary();
    const trends = createAccountTrends(account.accountId, {
      secondaryScheduled: [
        { t: "2026-01-01T00:00:00.000Z", v: 100 },
        { t: "2026-01-01T01:00:00.000Z", v: 99.4 },
      ],
    });

    render(<AccountUsagePanel account={account} trends={trends} />);

    expect(screen.getByText("Weekly plan")).toBeInTheDocument();
  });

  it("shows trends when only secondary scheduled trend points are present", () => {
    const account = createAccountSummary();
    const trends = createAccountTrends(account.accountId, {
      primary: [],
      secondary: [],
      secondaryScheduled: [
        { t: "2026-01-01T00:00:00.000Z", v: 100 },
        { t: "2026-01-01T06:00:00.000Z", v: 92 },
      ],
    });

    render(<AccountUsagePanel account={account} trends={trends} />);

    expect(screen.getByText("7-day trend")).toBeInTheDocument();
    expect(screen.getByText("Weekly plan")).toBeInTheDocument();
  });
});
