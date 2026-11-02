import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SubscriptionRemaining } from "@/features/accounts/components/subscription-remaining";
import i18n from "@/i18n";
import { formatDateTimeInline } from "@/utils/formatters";

describe("SubscriptionRemaining", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T12:00:00.000Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it.each(["default", "iso8601"] as const)("shows both countdowns and dates in %s format", (displayFormat) => {
    const activeUntil = "2026-01-03T12:00:00.000Z";
    const offsetUntil = "2026-01-17T12:00:00.000Z";

    render(<SubscriptionRemaining activeUntil={activeUntil} displayFormat={displayFormat} />);

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 2d / +14d: 16d");
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute(
      "title",
      expect.stringContaining(formatDateTimeInline(activeUntil, displayFormat)),
    );
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute(
      "title",
      expect.stringContaining(formatDateTimeInline(offsetUntil, displayFormat)),
    );
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute(
      "title",
      expect.stringContaining("reference only, not a confirmed extension"),
    );
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute(
      "title",
      expect.stringContaining("Saved authorization metadata may lag renewal."),
    );
  });

  it("updates the countdown each minute without changing account data", () => {
    render(
      <SubscriptionRemaining activeUntil="2026-01-01T12:59:00.000Z" displayFormat="default" />,
    );

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 59m / +14d: 14d");
    act(() => {
      vi.advanceTimersByTime(60_000);
    });

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 58m / +14d: 14d");
  });

  it("labels an elapsed recorded period without claiming access ended", () => {
    render(
      <SubscriptionRemaining activeUntil="2025-12-31T12:00:00.000Z" displayFormat="default" />,
    );

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Recorded period ended / +14d: 13d");
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute(
      "title",
      expect.stringContaining(formatDateTimeInline("2026-01-14T12:00:00.000Z", "default")),
    );
  });

  it.each([
    ["2026-01-01T12:01:00.000Z", "Subscription: 1m / +14d: 14d", "Recorded period ended / +14d: 14d"],
    ["2025-12-18T12:01:00.000Z", "Recorded period ended / +14d: 1m", "Recorded period ended / +14d: elapsed"],
  ])("updates expiry independently for %s", (activeUntil, before, after) => {
    render(<SubscriptionRemaining activeUntil={activeUntil} displayFormat="default" />);

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent(before);
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent(after);
  });

  it("labels both elapsed dates without claiming access ended", () => {
    render(<SubscriptionRemaining activeUntil="2025-12-17T12:00:00.000Z" displayFormat="default" />);

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Recorded period ended / +14d: elapsed");
  });

  it.each([null, undefined, "", "not-a-date"])("shows %s as unknown without a derived date", (activeUntil) => {
    render(<SubscriptionRemaining activeUntil={activeUntil} displayFormat="default" />);

    expect(screen.getByTestId("subscription-remaining").textContent).toBe("Subscription: unknown");
    expect(screen.getByTestId("subscription-remaining")).not.toHaveAttribute("title");
  });

  it.each([
    ["ko", "구독: 2d / +14일: 16d", "기록된 기간 종료 / +14일: 경과"],
    ["zh-CN", "订阅：2d / +14天：16d", "记录周期已结束 / +14天：已过"],
  ])("localizes both labels in %s", async (language, future, ended) => {
    await i18n.changeLanguage(language);
    try {
      const { rerender } = render(<SubscriptionRemaining activeUntil="2026-01-03T12:00:00.000Z" displayFormat="default" />);

      expect(screen.getByTestId("subscription-remaining")).toHaveTextContent(future);
      rerender(<SubscriptionRemaining activeUntil="2025-12-17T12:00:00.000Z" displayFormat="default" />);
      expect(screen.getByTestId("subscription-remaining")).toHaveTextContent(ended);
    } finally {
      await act(async () => { await i18n.changeLanguage("en"); });
    }
  });

  it("counts down to the end ChatGPT reported instead of the recorded end", () => {
    const subscription = {
      activeUntil: "2026-01-05T12:00:00.000Z",
      willRenew: true,
      observedAt: "2026-01-01T11:50:00.000Z",
    };

    render(
      <SubscriptionRemaining activeUntil="2025-12-01T12:00:00.000Z" subscription={subscription} displayFormat="default" />,
    );

    const element = screen.getByTestId("subscription-remaining");
    expect(element).toHaveTextContent("Subscription: 4d / +14d: 18d");
    expect(element).toHaveAttribute("title", expect.stringContaining(
      `End from ChatGPT: ${formatDateTimeInline(subscription.activeUntil, "default")}`,
    ));
    expect(element).toHaveAttribute("title", expect.stringContaining(
      `Renews automatically, checked ${formatDateTimeInline(subscription.observedAt, "default")}`,
    ));
    expect(element).toHaveAttribute("title", expect.stringContaining(formatDateTimeInline("2026-01-19T12:00:00.000Z", "default")));
    expect(element).not.toHaveAttribute("title", expect.stringContaining("may lag renewal"));
  });

  it.each([
    [false, "Will not renew"],
    [null, "Renewal unknown"],
  ])("names renewal %s in the tooltip", (willRenew, renewal) => {
    render(
      <SubscriptionRemaining
        activeUntil={null}
        subscription={{ activeUntil: "2026-01-03T12:00:00.000Z", willRenew, observedAt: "2026-01-01T11:00:00.000Z" }}
        displayFormat="default"
      />,
    );

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 2d / +14d: 16d");
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute("title", expect.stringContaining(renewal));
  });

  it.each([
    ["2025-12-31T12:30:00.000Z", "Subscription ended / +14d: 13d"],
    ["2025-12-31T11:30:00.000Z", "Recorded period ended / +14d: 13d"],
  ])("says the subscription ended only when a read at %s confirms it", (observedAt, text) => {
    render(
      <SubscriptionRemaining
        activeUntil="2026-02-01T12:00:00.000Z"
        subscription={{ activeUntil: "2025-12-31T12:00:00.000Z", willRenew: true, observedAt }}
        displayFormat="default"
      />,
    );

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent(text);
  });

  it("falls back to the recorded end when the read has no valid date", () => {
    render(
      <SubscriptionRemaining
        activeUntil="2026-01-03T12:00:00.000Z"
        subscription={{ activeUntil: "not-a-date", willRenew: true, observedAt: "2026-01-01T11:00:00.000Z" }}
        displayFormat="default"
      />,
    );

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 2d / +14d: 16d");
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute("title", expect.stringContaining("may lag renewal"));
  });

  it("localizes the confirmed end", async () => {
    await i18n.changeLanguage("ko");
    try {
      render(
        <SubscriptionRemaining
          activeUntil={null}
          subscription={{ activeUntil: "2025-12-17T12:00:00.000Z", willRenew: false, observedAt: "2025-12-18T12:00:00.000Z" }}
          displayFormat="default"
        />,
      );

      expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("구독 종료 / +14일: 경과");
      expect(screen.getByTestId("subscription-remaining")).toHaveAttribute("title", expect.stringContaining("갱신되지 않음"));
    } finally {
      await act(async () => { await i18n.changeLanguage("en"); });
    }
  });

  it("updates when new subscription metadata arrives", () => {
    const { rerender } = render(<SubscriptionRemaining activeUntil={null} displayFormat="default" />);
    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: unknown");

    rerender(<SubscriptionRemaining activeUntil="2026-01-03T12:00:00.000Z" displayFormat="default" />);
    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 2d / +14d: 16d");
  });
});
