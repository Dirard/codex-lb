import type { AccountGroup } from "@/features/account-groups/schemas";
import type { AccountSummary } from "@/features/accounts/schemas";
import type { ApiKey } from "@/features/api-keys/schemas";
import {
  GroupsSummarySchema,
  type GroupAccount,
  type GroupKey,
  type GroupSummaryRow,
  type GroupsSummary,
} from "@/features/groups/schemas";
import type { HealthCounts } from "@/features/overview/schemas";
import { createGroupRow, createShellStatus } from "@/test/mocks/overview-factories";

const zeroHealth: HealthCounts = { ok: 0, low: 0, limited: 0, reauth: 0, paused: 0, off: 0 };

function healthOf(status: string): GroupAccount["health"] {
  switch (status) {
    case "paused":
      return "paused";
    case "reauth_required":
      return "reauth";
    case "rate_limited":
    case "quota_exceeded":
      return "limited";
    case "deactivated":
      return "off";
  }
  return "ok";
}

function groupKeyOf(key: ApiKey, index: number): GroupKey {
  const spendToday = 0.5 * (index + 1);
  return {
    id: key.id,
    name: key.name,
    prefix: key.keyPrefix,
    status: key.isActive ? "active" : "disabled",
    spendTodayUsd: spendToday,
    spend7dUsd: spendToday * 5,
    spend30dUsd: spendToday * 20,
    maxLimitPercent: key.limits.length > 0 ? 40 : null,
    lastUsedAt: key.lastUsedAt ?? null,
  };
}

function rowOf(base: Partial<GroupSummaryRow>, accounts: GroupAccount[], keys: ApiKey[]): GroupSummaryRow {
  const health = { ...zeroHealth };
  for (const account of accounts) {
    health[account.health] += 1;
  }
  const topKeys7d = keys.map(groupKeyOf).sort((a, b) => (b.spend7dUsd ?? 0) - (a.spend7dUsd ?? 0)).slice(0, 20);
  const spendToday = topKeys7d.reduce((sum, key) => sum + key.spendTodayUsd, 0);
  return {
    ...createGroupRow({
      accounts: health,
      keys: { active: keys.filter((key) => key.isActive).length, total: keys.length },
      spendTodayUsd: spendToday,
      spend7dUsd: spendToday * 5,
    }),
    spend30dUsd: spendToday * 20,
    limits: [],
    accountIds: [],
    topKeys7d,
    ...base,
  } as GroupSummaryRow;
}

/** The groups summary of the mock state: one row per group, the ungrouped row and the pool. */
export function createGroupsSummary(accounts: AccountSummary[], apiKeys: ApiKey[], groups: AccountGroup[]): GroupsSummary {
  const groupAccounts: GroupAccount[] = accounts.map((account) => ({
    id: account.accountId,
    label: account.alias || account.email || account.accountId,
    email: account.email ?? "",
    kind: "chatgpt",
    status: account.status,
    health: healthOf(account.status),
    primaryRemainingPercent: account.usage?.primaryRemainingPercent ?? null,
    secondaryRemainingPercent: account.usage?.secondaryRemainingPercent ?? null,
  }));
  const rows = groups.map((group) =>
    rowOf(
      {
        id: group.id,
        name: group.name,
        accountIds: group.accountIds,
        limits: group.limits.map((rule) => ({ ...rule, modelFilter: rule.modelFilter ?? null })),
      },
      groupAccounts.filter((account) => group.accountIds.includes(account.id)),
      apiKeys.filter((key) => key.groupId === group.id),
    ),
  );
  rows.push(rowOf({ id: null, name: "Ungrouped keys" }, groupAccounts, apiKeys.filter((key) => !key.groupId)));
  return GroupsSummarySchema.parse({
    generatedAt: new Date().toISOString(),
    timezone: "UTC",
    shell: createShellStatus(),
    pool: rowOf({ id: null, name: "Pool", topKeys7d: [] }, groupAccounts, apiKeys),
    groups: rows,
    accounts: groupAccounts,
  });
}
