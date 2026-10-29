import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  consumeRateLimitResetCredit,
  consumeAccountUsageResetCredit,
  deleteAccount,
  exportAccountAuth,
  getAccountTrends,
  getAccountUsageResetCredits,
  getRateLimitResetCredits,
  importAccount,
  listAccounts,
  pauseAccount,
  probeAccount,
  reactivateAccount,
  setAccountAlias,
  setAccountGroups,
  updateAccount,
  updateAccountLimitWarmup,
  updateAccountRoutingPolicy,
} from "@/features/accounts/api";
import type {
  AccountRoutingPolicy,
  AccountsResponse,
  AccountSummary,
  AccountUsageResetConsumeResponse,
} from "@/features/accounts/schemas";
import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { invalidateEntityViews } from "@/lib/invalidate-entity-views";

export const ACCOUNTS_LIST_QUERY_KEY = ["accounts", "list"] as const;

/** The Accounts page's one periodic request (dashboard redesign use case 8). */
export const ACCOUNTS_POLL_MS = 30_000;

/**
 * Fetches the account list and writes the shell status it carries into the
 * shared shell query, so the Accounts page needs no other periodic request.
 */
export async function fetchAccounts(queryClient: QueryClient): Promise<AccountsResponse> {
  const response = await listAccounts();
  if (response.shell) {
    queryClient.setQueryData(SHELL_STATUS_QUERY_KEY, response.shell);
  }
  return response;
}

/**
 * Shows a mutation's result in the account's row at once; the refetch that
 * follows replaces it with the server's view (R45).
 */
export function patchAccountRow(queryClient: QueryClient, accountId: string, patch: Partial<AccountSummary>) {
  queryClient.setQueryData<AccountsResponse>(ACCOUNTS_LIST_QUERY_KEY, (current) =>
    current
      ? {
          ...current,
          accounts: current.accounts.map((account) => (account.accountId === accountId ? { ...account, ...patch } : account)),
        }
      : current,
  );
}

async function invalidateAccountRelatedQueries(queryClient: ReturnType<typeof useQueryClient>, accountId?: string) {
  const invalidations = [
    queryClient.invalidateQueries({ queryKey: ["accounts", "list"] }),
    queryClient.invalidateQueries({ queryKey: ["dashboard", "overview"] }),
    queryClient.invalidateQueries({ queryKey: ["dashboard", "projections"] }),
    invalidateEntityViews(queryClient),
  ];
  if (accountId) {
    invalidations.push(queryClient.invalidateQueries({ queryKey: ["accounts", "trends", accountId] }));
    invalidations.push(queryClient.invalidateQueries({ queryKey: ["accounts", "reset-credits", accountId] }));
  } else {
    invalidations.push(queryClient.invalidateQueries({ queryKey: ["accounts", "trends"] }));
    invalidations.push(queryClient.invalidateQueries({ queryKey: ["accounts", "reset-credits"] }));
  }
  await Promise.all(invalidations);
}

function usageResetToastMessage(data: AccountUsageResetConsumeResponse, t: ReturnType<typeof useTranslation>["t"]): string {
  const changed =
    data.primaryUsedPercentBefore !== data.primaryUsedPercentAfter ||
    data.secondaryUsedPercentBefore !== data.secondaryUsedPercentAfter ||
    data.accountStatusBefore !== data.accountStatusAfter;
  if (data.code === "reset") {
    return changed ? t("accounts.toasts.usageResetApplied") : t("accounts.toasts.usageResetAppliedUnchanged");
  }
  if (data.code === "already_redeemed") {
    return t("accounts.toasts.usageResetAlreadyApplied");
  }
  if (data.code === "no_credit") {
    return t("accounts.toasts.noUsageResetCredits");
  }
  if (data.code === "nothing_to_reset") {
    return t("accounts.toasts.nothingToReset");
  }
  return t("accounts.toasts.usageResetCompleted");
}

function createRedeemRequestId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `dashboard-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

/**
 * Account mutation actions without the polling query.
 * Use this when you need account actions but already have account data
 * from another source (e.g. the dashboard overview query).
 */
export function useAccountMutations() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const usageResetRedeemRequestRef = useRef<{
    accountId: string;
    redeemRequestId: string;
  } | null>(null);

  const importMutation = useMutation({
    mutationFn: importAccount,
    onSuccess: () => {
      toast.success(t("accounts.toasts.imported"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.importFailed"));
    },
  });

  const pauseMutation = useMutation({
    mutationFn: pauseAccount,
    onSuccess: (_data, accountId) => {
      patchAccountRow(queryClient, accountId, { status: "paused" });
      toast.success(t("accounts.toasts.paused"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.pauseFailed"));
    },
  });

  const resumeMutation = useMutation({
    mutationFn: reactivateAccount,
    onSuccess: (_data, accountId) => {
      patchAccountRow(queryClient, accountId, { status: "active" });
      toast.success(t("accounts.toasts.resumed"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.resumeFailed"));
    },
  });

  const setAliasMutation = useMutation({
    mutationFn: ({ accountId, alias }: { accountId: string; alias: string | null }) =>
      setAccountAlias(accountId, alias),
    onSuccess: (data) => {
      const account = queryClient
        .getQueryData<AccountsResponse>(ACCOUNTS_LIST_QUERY_KEY)
        ?.accounts.find((item) => item.accountId === data.accountId);
      patchAccountRow(queryClient, data.accountId, { alias: data.alias, displayName: data.alias || account?.email || data.accountId });
      toast.success(t("accounts.toasts.aliasUpdated"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.aliasUpdateFailed"));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: ({ accountId, deleteHistory }: { accountId: string; deleteHistory: boolean }) =>
      deleteAccount(accountId, deleteHistory),
    onSuccess: () => {
      toast.success(t("accounts.toasts.deleted"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.deleteFailed"));
    },
  });

  const probeMutation = useMutation({
    mutationFn: ({ accountId, model }: { accountId: string; model?: string }) =>
      probeAccount(accountId, model ? { model } : undefined),
    onSuccess: (_data, variables) => {
      toast.success(t("accounts.toasts.probed"));
      void invalidateAccountRelatedQueries(queryClient, variables.accountId);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.probeFailed"));
    },
  });

  const usageResetMutation = useMutation({
    mutationFn: ({ accountId }: { accountId: string }) => {
      if (usageResetRedeemRequestRef.current?.accountId !== accountId) {
        usageResetRedeemRequestRef.current = {
          accountId,
          redeemRequestId: createRedeemRequestId(),
        };
      }
      return consumeAccountUsageResetCredit(accountId, {
        redeemRequestId: usageResetRedeemRequestRef.current.redeemRequestId,
      });
    },
    onSuccess: async (data, variables) => {
      usageResetRedeemRequestRef.current = null;
      await invalidateAccountRelatedQueries(queryClient, variables.accountId);
      toast.success(usageResetToastMessage(data, t));
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.usageResetFailed"));
    },
  });

  const limitWarmupMutation = useMutation({
    mutationFn: ({ accountId, enabled }: { accountId: string; enabled: boolean }) =>
      updateAccountLimitWarmup(accountId, enabled),
    onSuccess: (data, variables) => {
      patchAccountRow(queryClient, variables.accountId, { limitWarmupEnabled: data.enabled });
      toast.success(data.enabled ? t("accounts.toasts.limitWarmupEnabled") : t("accounts.toasts.limitWarmupDisabled"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.limitWarmupUpdateFailed"));
    },
  });

  const routingPolicyMutation = useMutation({
    mutationFn: ({
      accountId,
      routingPolicy,
    }: {
      accountId: string;
      routingPolicy: AccountRoutingPolicy;
    }) => updateAccountRoutingPolicy(accountId, routingPolicy),
    onSuccess: (data) => {
      patchAccountRow(queryClient, data.accountId, { routingPolicy: data.routingPolicy });
      const label =
        data.routingPolicy === "normal" ? "normal" : data.routingPolicy.replace("_", "-");
      toast.success(t("accounts.toasts.routingPolicySet", { label }));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.routingPolicyUpdateFailed"));
    },
  });

  const exportAuthMutation = useMutation({
    mutationFn: exportAccountAuth,
    onSuccess: () => {
      toast.success(t("accounts.toasts.exported"));
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.exportFailed"));
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ accountId, securityWorkAuthorized }: { accountId: string; securityWorkAuthorized: boolean }) =>
      updateAccount(accountId, { securityWorkAuthorized }),
    onSuccess: (_data, variables) => {
      patchAccountRow(queryClient, variables.accountId, { securityWorkAuthorized: variables.securityWorkAuthorized });
      toast.success(t("accounts.toasts.updated"));
      void invalidateAccountRelatedQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.updateFailed"));
    },
  });

  const resetCreditConsumeMutation = useMutation({
    mutationFn: ({ accountId, redeemRequestId }: { accountId: string; redeemRequestId?: string }) =>
      consumeRateLimitResetCredit(accountId, redeemRequestId ? { redeemRequestId } : undefined),
    onSuccess: (data) => {
      const resetCount = data.windowsReset ?? 0;
      toast.success(
        t("accounts.toasts.rateLimitWindowsReset", { count: resetCount }),
      );
      void queryClient.invalidateQueries({ queryKey: ["accounts", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["accounts", "trends"] });
      void queryClient.invalidateQueries({ queryKey: ["accounts", "reset-credits"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard", "overview"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard", "projections"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.resetCreditRedeemFailed"));
    },
  });

  return {
    importMutation,
    pauseMutation,
    resumeMutation,
    setAliasMutation,
    deleteMutation,
    probeMutation,
    usageResetMutation,
    exportAuthMutation,
    limitWarmupMutation,
    routingPolicyMutation,
    updateMutation,
    resetCreditConsumeMutation,
  };
}

export function useRateLimitResetCredits(
  accountId: string | null,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["accounts", "reset-credits", accountId],
    queryFn: () => getRateLimitResetCredits(accountId as string),
    enabled: enabled && !!accountId,
    staleTime: 0,
  });
}

export function useAccountTrends(accountId: string | null) {
  return useQuery({
    queryKey: ["accounts", "trends", accountId],
    queryFn: () => getAccountTrends(accountId!),
    enabled: !!accountId,
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    refetchIntervalInBackground: false,
  });
}

/** A reset-credit count checked with the provider, and when. */
export type CheckedResetCredits = {
  accountId: string;
  availableCount: number;
  checkedAt: string;
};

/**
 * Check now: the only request of the Accounts page that refreshes usage from
 * the provider (`GET /api/accounts/{id}/usage-reset-credits`, R42). The
 * refreshed quotas reach the row through the list refetch.
 */
export function useCheckResetCredits() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (accountId: string): Promise<CheckedResetCredits> => {
      const response = await getAccountUsageResetCredits(accountId);
      return { accountId, availableCount: response.rateLimitResetCredits.availableCount, checkedAt: new Date().toISOString() };
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ACCOUNTS_LIST_QUERY_KEY });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.sheet.checkFailed"));
    },
  });
}

/** Replaces an account's groups (`PUT /api/accounts/{id}/groups`). */
export function useSetAccountGroups() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ accountId, groupIds }: { accountId: string; groupIds: string[] }) => setAccountGroups(accountId, groupIds),
    onSuccess: (data) => {
      patchAccountRow(queryClient, data.accountId, { groups: [...data.groupIds].sort() });
      toast.success(t("accounts.toasts.groupsUpdated"));
      void queryClient.invalidateQueries({ queryKey: ACCOUNTS_LIST_QUERY_KEY });
      void queryClient.invalidateQueries({ queryKey: ["account-groups", "list"] });
      void invalidateEntityViews(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accounts.toasts.groupsUpdateFailed"));
    },
  });
}

export function useAccounts() {
  const queryClient = useQueryClient();
  const { data, error, isFetching, isLoading, isPending, isSuccess, refetch } = useQuery({
    queryKey: ACCOUNTS_LIST_QUERY_KEY,
    queryFn: () => fetchAccounts(queryClient),
    select: (data) => data.accounts,
    refetchInterval: ACCOUNTS_POLL_MS,
    refetchIntervalInBackground: false,
  });
  const accountsQuery = { data, error, isFetching, isLoading, isPending, isSuccess, refetch };

  const mutations = useAccountMutations();

  return { accountsQuery, ...mutations };
}
