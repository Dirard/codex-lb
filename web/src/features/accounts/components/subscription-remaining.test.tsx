import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SubscriptionRemaining } from "@/features/accounts/components/subscription-remaining";
import { formatDateTimeInline } from "@/utils/formatters";

describe("SubscriptionRemaining", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T12:00:00.000Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows a future countdown and the recorded date tooltip", () => {
    const activeUntil = "2026-01-03T12:00:00.000Z";

    render(<SubscriptionRemaining activeUntil={activeUntil} displayFormat="default" />);

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 2d");
    expect(screen.getByTestId("subscription-remaining")).toHaveAttribute(
      "title",
      expect.stringContaining(formatDateTimeInline(activeUntil, "default")),
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

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 59m");
    act(() => {
      vi.advanceTimersByTime(60_000);
    });

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: 58m");
  });

  it("labels an elapsed recorded period without claiming access ended", () => {
    render(
      <SubscriptionRemaining activeUntil="2025-12-31T12:00:00.000Z" displayFormat="default" />,
    );

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Recorded period ended");
  });

  it("shows missing and invalid dates as unknown", () => {
    const { rerender } = render(<SubscriptionRemaining activeUntil={null} displayFormat="default" />);

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: unknown");

    rerender(<SubscriptionRemaining activeUntil="not-a-date" displayFormat="default" />);

    expect(screen.getByTestId("subscription-remaining")).toHaveTextContent("Subscription: unknown");
  });
});
