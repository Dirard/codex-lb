import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PoolRunwayCard } from "@/features/groups/components/pool-runway-card";
import type { ServerWeeklyCreditPace } from "@/features/dashboard/schemas";

const pace = {
  totalFullCredits: 50_400,
  totalActualRemainingCredits: 2_000,
  totalExpectedRemainingCredits: 20_000,
  actualUsedPercent: 96,
  scheduledUsedPercent: 60,
  deltaPercent: 36,
  scheduleGapCredits: 18_000,
  overPlanCredits: 18_000,
  projectedShortfallCredits: 9_000,
  pauseForBreakEvenHours: null,
  paceMultiplier: 1.6,
  throttleToPercent: 55,
  reduceByPercent: 45,
  proAccountEquivalentToCoverOverPlan: 0.2,
  proAccountsToCoverOverPlan: 1,
  projectedDepletionHours: 6,
  projectedMinimumRemainingCredits: null,
  forecastBurnRateCreditsPerHour: 330,
  scheduledBurnRateCreditsPerHour: 300,
  headroomPercent: 4,
  headroomCredits: 2_000,
  burnRateRecentCreditsPerHour: 330,
  depletionEtaHours: 6,
  nextReliefInHours: 20,
  nextReliefCredits: 25_200,
  resetEvents: [{ at: new Date(Date.now() + 20 * 3_600_000).toISOString(), creditsReturned: 25_200 }],
  runwayStatus: "runs_dry",
  addProAccounts: 1,
  status: "danger",
  accountCount: 2,
  staleAccountCount: 0,
  inactiveAccountCount: 0,
  confidence: "high",
} as ServerWeeklyCreditPace;

describe("PoolRunwayCard", () => {
  it("shows the headroom, the runway and both recommendations when the pool runs dry (Q2)", () => {
    render(<PoolRunwayCard pace={pace} loading={false} />);
    expect(screen.getByText("Runs dry")).toBeInTheDocument();
    expect(screen.getByText("4.0%")).toBeInTheDocument();
    expect(screen.getByText("runs out in ~6h")).toBeInTheDocument();
    expect(screen.getByText("relief in 20h — arrives after depletion")).toBeInTheDocument();
    expect(screen.getByText("~55% of current load")).toBeInTheDocument();
    expect(screen.getByText("Add 1 Pro account")).toBeInTheDocument();
  });

  it("says so when no pace is available", () => {
    render(<PoolRunwayCard pace={null} loading={false} />);
    expect(screen.getByText("No weekly credit pace is available yet.")).toBeInTheDocument();
    expect(screen.queryByTestId("pool-runway-recommendations")).not.toBeInTheDocument();
  });
});
