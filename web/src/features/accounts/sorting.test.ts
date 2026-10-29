import { describe, expect, it } from "vitest";

import {
  DEFAULT_ACCOUNT_FILTERS,
  DEFAULT_ACCOUNT_SORT_MODE,
  filterAccounts,
  sortAccountsForDisplay,
  type AccountSortMode,
} from "@/features/accounts/sorting";
import type { AccountSummary } from "@/features/accounts/schemas";
import { createAccountSummary } from "@/test/mocks/factories";

const NOW = Date.parse("2026-01-01T00:00:00.000Z");
const at = (minutes: number) => new Date(NOW + minutes * 60_000).toISOString();

function ids(accounts: AccountSummary[], mode: AccountSortMode): string[] {
  return sortAccountsForDisplay(accounts, mode, NOW).map((account) => account.accountId);
}

describe("sortAccountsForDisplay", () => {
  it("orders by status severity by default, then by the next reset", () => {
    expect(DEFAULT_ACCOUNT_SORT_MODE).toBe("status_severity");
    const accounts = [
      createAccountSummary({ accountId: "active-late", status: "active", resetAtPrimary: at(90), resetAtSecondary: at(900) }),
      createAccountSummary({ accountId: "paused", status: "paused" }),
      createAccountSummary({ accountId: "limited", status: "rate_limited" }),
      createAccountSummary({ accountId: "reauth", status: "reauth_required" }),
      createAccountSummary({ accountId: "exceeded", status: "quota_exceeded" }),
      createAccountSummary({ accountId: "deactivated", status: "deactivated" }),
      createAccountSummary({ accountId: "active-soon", status: "active", resetAtPrimary: at(10), resetAtSecondary: at(900) }),
    ];

    expect(sortAccountsForDisplay(accounts, undefined, NOW).map((account) => account.accountId)).toEqual([
      "reauth", "deactivated", "exceeded", "limited", "paused", "active-soon", "active-late",
    ]);
  });

  it("sorts by next reset in both directions, accounts without one last", () => {
    const accounts = [
      createAccountSummary({ accountId: "none", resetAtPrimary: null, resetAtSecondary: null }),
      createAccountSummary({ accountId: "late", resetAtPrimary: at(300), resetAtSecondary: at(5000) }),
      createAccountSummary({ accountId: "soon", resetAtPrimary: at(20), resetAtSecondary: at(5000) }),
      createAccountSummary({ accountId: "monthly", resetAtPrimary: null, resetAtSecondary: null, resetAtMonthly: at(60) }),
      createAccountSummary({ accountId: "elapsed", resetAtPrimary: at(-5), resetAtSecondary: at(8000) }),
    ];

    expect(ids(accounts, "reset_soonest")).toEqual(["soon", "monthly", "late", "elapsed", "none"]);
    expect(ids(accounts, "reset_latest")).toEqual(["elapsed", "late", "monthly", "soon", "none"]);
  });

  it("sorts by the remaining of a window, lowest first and missing windows last", () => {
    const accounts = [
      createAccountSummary({ accountId: "high", usage: { primaryRemainingPercent: 90, secondaryRemainingPercent: 10 } }),
      createAccountSummary({ accountId: "low", usage: { primaryRemainingPercent: 5, secondaryRemainingPercent: 70 } }),
      createAccountSummary({ accountId: "weekly-only", usage: { primaryRemainingPercent: null, secondaryRemainingPercent: 40 } }),
    ];

    expect(ids(accounts, "primary_remaining")).toEqual(["low", "high", "weekly-only"]);
    expect(ids(accounts, "secondary_remaining")).toEqual(["high", "weekly-only", "low"]);
  });

  it("sorts by alias, else e-mail, in both directions", () => {
    const accounts = [
      createAccountSummary({ accountId: "b", alias: null, email: "beta@example.com", displayName: "beta@example.com" }),
      createAccountSummary({ accountId: "a", alias: "Alpha", email: "zed@example.com", displayName: "Alpha" }),
      createAccountSummary({ accountId: "c", alias: null, email: "carol@example.com", displayName: "carol@example.com" }),
    ];

    expect(ids(accounts, "name_asc")).toEqual(["a", "b", "c"]);
    expect(ids(accounts, "name_desc")).toEqual(["c", "b", "a"]);
  });

  it("orders most reset credits first, ties by soonest expiry, missing expiry last", () => {
    const accounts = [
      createAccountSummary({ accountId: "one", availableResetCredits: 1 }),
      createAccountSummary({ accountId: "three-none", availableResetCredits: 3, resetCreditNearestExpiresAt: null }),
      createAccountSummary({ accountId: "three-later", availableResetCredits: 3, resetCreditNearestExpiresAt: "2026-02-01T00:00:00.000Z" }),
      createAccountSummary({ accountId: "three-sooner", availableResetCredits: 3, resetCreditNearestExpiresAt: "2026-01-10T00:00:00.000Z" }),
    ];

    expect(ids(accounts, "most_reset_credits")).toEqual(["three-sooner", "three-later", "three-none", "one"]);
  });
});

describe("filterAccounts", () => {
  const accounts = [
    createAccountSummary({ accountId: "acc-alpha", alias: "Alpha", email: "a@example.com", status: "active", groups: ["g1"] }),
    createAccountSummary({ accountId: "acc-beta", alias: null, email: "Beta@Example.com", status: "paused", groups: [] }),
    createAccountSummary({ accountId: "acc-gamma", alias: null, email: "c@example.com", status: "active", groups: ["g1", "g2"] }),
  ];
  const matching = (filters: Partial<typeof DEFAULT_ACCOUNT_FILTERS>) =>
    filterAccounts(accounts, { ...DEFAULT_ACCOUNT_FILTERS, ...filters }).map((account) => account.accountId);

  it("searches alias, e-mail and id case-insensitively", () => {
    expect(matching({ search: "alp" })).toEqual(["acc-alpha"]);
    expect(matching({ search: "beta@example" })).toEqual(["acc-beta"]);
    expect(matching({ search: "ACC-GAM" })).toEqual(["acc-gamma"]);
  });

  it("filters by status and by group, Ungrouped selecting accounts without one", () => {
    expect(matching({ status: "paused" })).toEqual(["acc-beta"]);
    expect(matching({ group: "g1" })).toEqual(["acc-alpha", "acc-gamma"]);
    expect(matching({ group: "g2" })).toEqual(["acc-gamma"]);
    expect(matching({ group: "ungrouped" })).toEqual(["acc-beta"]);
  });
});
