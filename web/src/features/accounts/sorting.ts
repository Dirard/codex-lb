import type { AccountSummary } from "@/features/accounts/schemas";
import { parseDate } from "@/utils/formatters";

export type AccountSortMode =
  | "status_severity"
  | "reset_soonest"
  | "reset_latest"
  | "primary_remaining"
  | "secondary_remaining"
  | "most_reset_credits"
  | "name_asc"
  | "name_desc";

export const ACCOUNT_SORT_OPTIONS: readonly { value: AccountSortMode; label: string }[] = [
  { value: "status_severity", label: "Status severity" },
  { value: "reset_soonest", label: "Reset time (soonest)" },
  { value: "reset_latest", label: "Reset time (latest)" },
  { value: "primary_remaining", label: "5h remaining" },
  { value: "secondary_remaining", label: "Weekly remaining" },
  { value: "most_reset_credits", label: "Most reset credits" },
  { value: "name_asc", label: "Name (A-Z)" },
  { value: "name_desc", label: "Name (Z-A)" },
] as const;

export const DEFAULT_ACCOUNT_SORT_MODE: AccountSortMode = "status_severity";

/** Problems first: accounts needing the operator, then the blocked ones. */
const STATUS_SEVERITY: Record<string, number> = {
  reauth_required: 0,
  deactivated: 1,
  quota_exceeded: 2,
  rate_limited: 3,
  paused: 4,
  active: 5,
};

export function statusSeverity(status: string): number {
  return STATUS_SEVERITY[status] ?? STATUS_SEVERITY.active;
}

/** The label the table shows and name sorting uses: alias, else e-mail. */
export function accountLabel(account: AccountSummary): string {
  return (account.alias?.trim() || account.displayName || account.email || account.accountId).trim();
}

function upcomingResets(account: AccountSummary, nowMs: number): number[] {
  return [account.resetAtPrimary, account.resetAtSecondary, account.resetAtMonthly]
    .map((value) => parseDate(value)?.getTime() ?? Number.POSITIVE_INFINITY)
    .filter((resetAt) => resetAt > nowMs);
}

/** The earliest upcoming reset of the account's windows, or Infinity. */
export function nextReset(account: AccountSummary, nowMs = Date.now()): number {
  const resets = upcomingResets(account, nowMs);
  return resets.length > 0 ? Math.min(...resets) : Number.POSITIVE_INFINITY;
}

/** Finite values first, in the direction; missing values (Infinity) last. */
function compareFinite(left: number, right: number, direction: "asc" | "desc"): number {
  const leftFinite = Number.isFinite(left);
  const rightFinite = Number.isFinite(right);
  if (leftFinite !== rightFinite) {
    return leftFinite ? -1 : 1;
  }
  if (left === right) {
    return 0;
  }
  return direction === "desc" ? right - left : left - right;
}

function remaining(value: number | null | undefined): number {
  return value ?? Number.POSITIVE_INFINITY;
}

function resetCreditNearestExpiry(account: AccountSummary): number {
  return parseDate(account.resetCreditNearestExpiresAt)?.getTime() ?? Number.POSITIVE_INFINITY;
}

function compareByMode(left: AccountSummary, right: AccountSummary, mode: AccountSortMode, nowMs: number): number {
  switch (mode) {
    case "status_severity":
      return statusSeverity(left.status) - statusSeverity(right.status) ||
        compareFinite(nextReset(left, nowMs), nextReset(right, nowMs), "asc");
    case "reset_soonest":
      return compareFinite(nextReset(left, nowMs), nextReset(right, nowMs), "asc");
    case "reset_latest":
      return compareFinite(nextReset(left, nowMs), nextReset(right, nowMs), "desc");
    case "primary_remaining":
      return compareFinite(remaining(left.usage?.primaryRemainingPercent), remaining(right.usage?.primaryRemainingPercent), "asc");
    case "secondary_remaining":
      return compareFinite(remaining(left.usage?.secondaryRemainingPercent), remaining(right.usage?.secondaryRemainingPercent), "asc");
    case "most_reset_credits":
      return (right.availableResetCredits ?? 0) - (left.availableResetCredits ?? 0) ||
        compareFinite(resetCreditNearestExpiry(left), resetCreditNearestExpiry(right), "asc");
    case "name_asc":
      return accountLabel(left).toLowerCase().localeCompare(accountLabel(right).toLowerCase());
    case "name_desc":
      return accountLabel(right).toLowerCase().localeCompare(accountLabel(left).toLowerCase());
  }
  return 0;
}

/**
 * Orders accounts by the selected mode; ties fall back to the next reset,
 * then the label and the account id, so the order is stable between polls.
 */
export function sortAccountsForDisplay(
  accounts: AccountSummary[],
  sortMode: AccountSortMode = DEFAULT_ACCOUNT_SORT_MODE,
  nowMs = Date.now(),
): AccountSummary[] {
  return accounts.slice().sort((left, right) =>
    compareByMode(left, right, sortMode, nowMs) ||
    compareFinite(nextReset(left, nowMs), nextReset(right, nowMs), "asc") ||
    accountLabel(left).toLowerCase().localeCompare(accountLabel(right).toLowerCase()) ||
    left.accountId.localeCompare(right.accountId),
  );
}

export type AccountFilters = {
  search: string;
  status: string;
  /** "all", "ungrouped" or a group id. */
  group: string;
};

export const DEFAULT_ACCOUNT_FILTERS: AccountFilters = { search: "", status: "all", group: "all" };

/** Search matches alias, e-mail, display name and id, case-insensitively. */
export function filterAccounts(accounts: AccountSummary[], filters: AccountFilters): AccountSummary[] {
  const needle = filters.search.trim().toLowerCase();
  return accounts.filter((account) => {
    if (filters.status !== "all" && account.status !== filters.status) {
      return false;
    }
    if (filters.group === "ungrouped" && account.groups.length > 0) {
      return false;
    }
    if (filters.group !== "all" && filters.group !== "ungrouped" && !account.groups.includes(filters.group)) {
      return false;
    }
    if (!needle) {
      return true;
    }
    return [account.alias ?? "", account.email, account.displayName, account.accountId].some((value) =>
      value.toLowerCase().includes(needle),
    );
  });
}
