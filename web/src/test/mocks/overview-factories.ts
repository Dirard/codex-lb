import type { ShellStatus } from "@/components/layout/shell-status";
import { ShellStatusSchema } from "@/components/layout/shell-status";
import type { AccountGroup } from "@/features/account-groups/schemas";
import type { AccountSummary } from "@/features/accounts/schemas";
import type { ApiKey } from "@/features/api-keys/schemas";
import {
  OverviewResponseSchema,
  type AttentionKey,
  type GroupRow,
  type OverviewResponse,
  type WindowHealth,
} from "@/features/overview/schemas";
import { DirectorySchema, type Directory } from "@/features/search/api";

const NOW = Date.now();
const iso = (offsetMinutes: number) => new Date(NOW + offsetMinutes * 60_000).toISOString();

export function createShellStatus(overrides: Partial<ShellStatus> = {}): ShellStatus {
  return ShellStatusSchema.parse({
    ready: true,
    lastSyncAt: iso(-1),
    usageFoldedAt: iso(0),
    rollupsUsable: true,
    reauthRequiredAccounts: 0,
    availableResetCredits: 0,
    showResetCreditBadges: true,
    apiKeyAuthEnabled: true,
    routingStrategy: "usage_weighted",
    stickyThreadsEnabled: false,
    preferEarlierResetAccounts: false,
    preferEarlierResetWindow: "primary",
    ...overrides,
  });
}

export function createWindowHealth(overrides: Partial<WindowHealth> = {}): WindowHealth {
  return {
    remainingPercent: 62,
    remainingCredits: 279,
    capacityCredits: 450,
    coveredAccounts: 2,
    totalAccounts: 2,
    nextResetAt: iso(90),
    fullResetAt: iso(180),
    burnCreditsPerHour: 20,
    projectedExhaustionAt: null,
    runsOutBeforeReset: false,
    ...overrides,
  };
}

const zeroHealth = { ok: 0, low: 0, limited: 0, reauth: 0, paused: 0, off: 0 };

export function createGroupRow(overrides: Partial<GroupRow> = {}): GroupRow {
  return {
    id: "grp_1",
    name: "Team A",
    attention: "none",
    accounts: { ...zeroHealth, ok: 2 },
    primary: createWindowHealth(),
    secondary: createWindowHealth({ remainingPercent: 80, nextResetAt: iso(3000) }),
    limitFill: { maxPercent: 40, keysAtOrAbove90: 0, keysBlocked: 0 },
    keys: { active: 1, total: 1 },
    spendTodayUsd: 3.5,
    spend7dUsd: 21,
    trend7d: [2, 3, 1, 4, 3, 4.5, 3.5],
    problemAccounts: [],
    topKeys: [],
    ...overrides,
  };
}

export function createAttentionKey(overrides: Partial<AttentionKey> = {}): AttentionKey {
  return {
    id: "key_1",
    name: "Expiring key",
    prefix: "sk-clb-exp",
    groupId: null,
    groupName: null,
    reasons: [{ reason: "expiring", severity: "warning", value: 48 }],
    spendTodayUsd: 1.25,
    spend7dUsd: 7.5,
    maxLimit: null,
    expiresAt: iso(48 * 60),
    lastUsedAt: iso(-5),
    primaryAction: "extend_expiry",
    ...overrides,
  };
}

export function createOverview(overrides: Partial<OverviewResponse> = {}): OverviewResponse {
  return OverviewResponseSchema.parse({
    generatedAt: iso(0),
    timezone: "UTC",
    shell: createShellStatus(),
    pool: {
      accounts: { ...zeroHealth, ok: 2 },
      externalSources: 0,
      primary: createWindowHealth(),
      secondary: createWindowHealth({ remainingPercent: 80, nextResetAt: iso(3000) }),
      spendTodayUsd: 4.75,
      spendYesterdaySameTimeUsd: 3.25,
      requestsToday: 120,
      errorRateLastHour: 0.012,
      spend7dUsd: 28.5,
      trend7d: [3, 4, 2, 5, 4, 5.75, 4.75],
    },
    alerts: [],
    groups: [createGroupRow(), createGroupRow({ id: null, name: "Ungrouped keys", spendTodayUsd: 1.25 })],
    attentionKeys: [],
    attentionKeysTotal: 0,
    ...overrides,
  });
}

/** The directory of the mock state: non-deleted keys, groups with counts and accounts. */
export function createDirectory(accounts: AccountSummary[], apiKeys: ApiKey[], groups: AccountGroup[]): Directory {
  const now = Date.now();
  return DirectorySchema.parse({
    keys: apiKeys.map((key) => ({
      id: key.id,
      name: key.name,
      prefix: key.keyPrefix,
      groupId: key.groupId ?? null,
      status: !key.isActive ? "disabled" : key.expiresAt && new Date(key.expiresAt).getTime() <= now ? "expired" : "active",
    })),
    groups: groups.map((group) => ({
      id: group.id,
      name: group.name,
      accountCount: group.accountIds.length,
      keyCount: apiKeys.filter((key) => key.groupId === group.id).length,
    })),
    accounts: accounts.map((account) => ({
      id: account.accountId,
      label: account.alias || account.email || account.accountId,
      email: account.email ?? "",
      status: account.status,
      kind: "chatgpt",
    })),
  });
}
