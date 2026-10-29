import { Suspense, lazy, useCallback, useMemo, useState } from "react";
import { Plus, Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { AlertMessage } from "@/components/alert-message";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { EmptyState } from "@/components/empty-state";
import { PasswordConfirmDialog } from "@/components/password-confirm-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { useDialogState } from "@/hooks/use-dialog-state";
import { AccountDetailSheet } from "@/features/accounts/components/account-detail-sheet";
import { AccountsSkeleton } from "@/features/accounts/components/accounts-skeleton";
import { AccountsTable } from "@/features/accounts/components/accounts-table";
import { AccountsToolbar } from "@/features/accounts/components/accounts-toolbar";
import { AddAccountDialog } from "@/features/accounts/components/add-account-dialog";
import { AuthExportDialog } from "@/features/accounts/components/auth-export-dialog";
import { ImportDialog } from "@/features/accounts/components/import-dialog";
import { ResetCreditConfirmDialog } from "@/features/accounts/components/reset-credit-confirm-dialog";
import { WindowsOauthHelp } from "@/features/accounts/components/windows-oauth-help";
import { useAccounts, useCheckResetCredits } from "@/features/accounts/hooks/use-accounts";
import { useOauth } from "@/features/accounts/hooks/use-oauth";
import type { AccountAuthExportResponse } from "@/features/accounts/schemas";
import {
  DEFAULT_ACCOUNT_FILTERS,
  DEFAULT_ACCOUNT_SORT_MODE,
  filterAccounts,
  sortAccountsForDisplay,
  type AccountFilters,
  type AccountSortMode,
} from "@/features/accounts/sorting";
import { usePasswordConfirmationRequired } from "@/features/auth/hooks/use-password-confirmation";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { useSettings } from "@/features/settings/hooks/use-settings";
import { getErrorMessageOrNull } from "@/utils/errors";

const OauthDialog = lazy(() =>
  import("@/features/accounts/components/oauth-dialog").then((m) => ({
    default: m.OauthDialog,
  })),
);

/**
 * The Accounts page (dashboard redesign use case 8): a toolbar and a table of
 * every account with inline actions, and the selected account's detail sheet
 * (`?selected=<id>`). Its one periodic request is `GET /api/accounts`, which
 * carries the shell status; nothing on load or selection calls a provider.
 */
export function AccountsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [filters, setFilters] = useState<AccountFilters>(DEFAULT_ACCOUNT_FILTERS);
  const [sortMode, setSortMode] = useState<AccountSortMode>(DEFAULT_ACCOUNT_SORT_MODE);
  const [helpOpen, setHelpOpen] = useState(false);
  const [chooserOpen, setChooserOpen] = useState(false);
  const [oauthAccountId, setOauthAccountId] = useState<string | null>(null);
  const {
    accountsQuery,
    importMutation,
    pauseMutation,
    resumeMutation,
    setAliasMutation,
    probeMutation,
    usageResetMutation,
    limitWarmupMutation,
    updateMutation,
    deleteMutation,
    routingPolicyMutation,
    exportAuthMutation,
  } = useAccounts();
  const checkMutation = useCheckResetCredits();
  const directoryQuery = useDirectory();
  const { settingsQuery } = useSettings();
  const oauth = useOauth();

  const importDialog = useDialogState();
  const oauthDialog = useDialogState();
  const deleteDialog = useDialogState<string>();
  type ResetCreditDialogTarget = { accountId: string; availableResetCredits: number };
  const resetCreditDialog = useDialogState<ResetCreditDialogTarget>();
  const usageResetDialog = useDialogState<string>();
  const exportDialog = useDialogState<AccountAuthExportResponse>();
  const exportConfirmDialog = useDialogState<string>();
  const passwordConfirmationRequired = usePasswordConfirmationRequired();
  const [deleteHistory, setDeleteHistory] = useState(false);

  const accounts = useMemo(() => accountsQuery.data ?? [], [accountsQuery.data]);
  const groups = useMemo(() => directoryQuery.data?.groups ?? [], [directoryQuery.data]);
  const groupNames = useMemo(() => new Map(groups.map((group) => [group.id, group.name])), [groups]);
  const rows = useMemo(
    () => sortAccountsForDisplay(filterAccounts(accounts, filters), sortMode),
    [accounts, filters, sortMode],
  );
  const showMonthly = useMemo(
    () => accounts.some((account) => account.windowMinutesMonthly != null || account.usage?.monthlyRemainingPercent != null || account.resetAtMonthly != null),
    [accounts],
  );
  const showResetCreditExpiryBadge = settingsQuery.data?.showResetCreditExpiryBadge ?? true;

  const selectedAccountId = searchParams.get("selected");
  const selectedAccount = useMemo(
    () => (selectedAccountId ? (accounts.find((account) => account.accountId === selectedAccountId) ?? null) : null),
    [accounts, selectedAccountId],
  );

  const openAccount = useCallback(
    (accountId: string) => {
      const next = new URLSearchParams(searchParams);
      next.set("selected", accountId);
      setSearchParams(next);
    },
    [searchParams, setSearchParams],
  );
  const closeAccount = useCallback(() => {
    const next = new URLSearchParams(searchParams);
    next.delete("selected");
    setSearchParams(next);
  }, [searchParams, setSearchParams]);

  const runExport = (accountId: string, currentPassword?: string) => {
    void exportAuthMutation
      .mutateAsync({ accountId, currentPassword })
      .then((result) => exportDialog.show(result))
      .catch(() => null);
  };
  const exportAuth = (accountId: string) => {
    if (passwordConfirmationRequired) {
      exportConfirmDialog.show(accountId);
    } else {
      runExport(accountId);
    }
  };
  const reauth = (accountId: string | null) => {
    setOauthAccountId(accountId);
    oauthDialog.show();
  };
  const redeemResetCredit = (accountId: string) => {
    const account = accounts.find((item) => item.accountId === accountId);
    resetCreditDialog.show({ accountId, availableResetCredits: account?.availableResetCredits ?? 0 });
  };

  const mutationBusy =
    importMutation.isPending ||
    pauseMutation.isPending ||
    resumeMutation.isPending ||
    setAliasMutation.isPending ||
    probeMutation.isPending ||
    usageResetMutation.isPending ||
    limitWarmupMutation.isPending ||
    deleteMutation.isPending ||
    routingPolicyMutation.isPending ||
    exportAuthMutation.isPending ||
    updateMutation.isPending;
  const listError = getErrorMessageOrNull(accountsQuery.error);

  return (
    <div className="animate-fade-in-up space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("accounts.page.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("accounts.page.subtitle")}</p>
      </div>

      {listError ? (
        <AlertMessage variant="error">
          <span className="flex flex-wrap items-center gap-2">
            {accountsQuery.data ? t("accounts.page.refreshFailed", { message: listError }) : listError}
            <Button type="button" size="sm" variant="outline" className="h-7" onClick={() => void accountsQuery.refetch()}>
              {t("common.actions.retry")}
            </Button>
          </span>
        </AlertMessage>
      ) : null}

      {!accountsQuery.data ? (
        listError ? null : <AccountsSkeleton />
      ) : (
        <>
          <AccountsToolbar
            filters={filters}
            onFiltersChange={setFilters}
            sortMode={sortMode}
            onSortModeChange={setSortMode}
            groups={groups}
            helpOpen={helpOpen}
            onHelpToggle={() => setHelpOpen((open) => !open)}
            onAddAccount={() => setChooserOpen(true)}
          />
          {helpOpen ? <WindowsOauthHelp /> : null}
          {accounts.length === 0 ? (
            <EmptyState
              icon={Users}
              title={t("accounts.list.emptyTitle")}
              description={t("accounts.list.emptyDescription")}
              action={
                <Button type="button" size="sm" className="gap-1.5" onClick={() => setChooserOpen(true)}>
                  <Plus className="h-3.5 w-3.5" />
                  {t("accounts.list.addAccount")}
                </Button>
              }
            />
          ) : rows.length === 0 ? (
            <EmptyState icon={Users} title={t("accounts.list.noMatches")} description={t("accounts.list.adjustFilters")} />
          ) : (
            <AccountsTable
              accounts={rows}
              groupNames={groupNames}
              showMonthly={showMonthly}
              busy={mutationBusy}
              selectedAccountId={selectedAccount?.accountId ?? null}
              onOpen={openAccount}
              onRoutingPolicyChange={(accountId, routingPolicy) => void routingPolicyMutation.mutateAsync({ accountId, routingPolicy })}
              onPause={(accountId) => void pauseMutation.mutateAsync(accountId)}
              onResume={(accountId) => void resumeMutation.mutateAsync(accountId)}
              onReauth={reauth}
              onProbe={(accountId) => void probeMutation.mutateAsync({ accountId })}
              onExportAuth={exportAuth}
              onRedeemResetCredit={redeemResetCredit}
              onDelete={(accountId) => deleteDialog.show(accountId)}
            />
          )}
          {accounts.length > 0 ? (
            <p className="px-1 text-[11px] leading-snug text-muted-foreground/80">{t("accounts.list.statusEligibilityNote")}</p>
          ) : null}
        </>
      )}

      <AccountDetailSheet
        account={selectedAccount}
        onClose={closeAccount}
        busy={mutationBusy}
        onPause={(accountId) => void pauseMutation.mutateAsync(accountId)}
        onResume={(accountId) => void resumeMutation.mutateAsync(accountId)}
        onProbe={(accountId) => void probeMutation.mutateAsync({ accountId })}
        onResetUsage={(accountId) => usageResetDialog.show(accountId)}
        onSetAlias={(accountId, alias) => setAliasMutation.mutateAsync({ accountId, alias })}
        onDelete={(accountId) => deleteDialog.show(accountId)}
        onReauth={reauth}
        onExportAuth={exportAuth}
        onResetCredit={redeemResetCredit}
        showResetCreditExpiryBadge={showResetCreditExpiryBadge}
        onLimitWarmupChange={(accountId, enabled) => void limitWarmupMutation.mutateAsync({ accountId, enabled })}
        onRoutingPolicyChange={(accountId, routingPolicy) => void routingPolicyMutation.mutateAsync({ accountId, routingPolicy })}
        onSecurityWorkAuthorizedChange={(accountId, enabled) =>
          void updateMutation.mutateAsync({ accountId, securityWorkAuthorized: enabled })
        }
        checkedResetCredits={checkMutation.data ?? null}
        checkingResetCredits={checkMutation.isPending}
        onCheckResetCredits={(accountId) => checkMutation.mutate(accountId)}
      />

      <AddAccountDialog
        open={chooserOpen}
        onOpenChange={setChooserOpen}
        onImport={() => importDialog.show()}
        onAddAccount={() => reauth(null)}
      />

      <ImportDialog
        open={importDialog.open}
        busy={importMutation.isPending}
        error={getErrorMessageOrNull(importMutation.error)}
        onOpenChange={importDialog.onOpenChange}
        onImport={async (file) => {
          await importMutation.mutateAsync(file);
        }}
      />

      <Suspense fallback={null}>
        <OauthDialog
          open={oauthDialog.open}
          state={oauth.state}
          onOpenChange={(open) => {
            oauthDialog.onOpenChange(open);
            if (!open) {
              setOauthAccountId(null);
            }
          }}
          onStart={async (method) => {
            await oauth.start(method, oauthAccountId ?? undefined);
          }}
          onComplete={async () => {
            await accountsQuery.refetch();
          }}
          onManualCallback={async (callbackUrl) => {
            await oauth.manualCallback(callbackUrl);
          }}
          onReset={oauth.reset}
        />
      </Suspense>

      <PasswordConfirmDialog
        open={exportConfirmDialog.open}
        title={t("accounts.exportConfirm.title")}
        description={t("accounts.exportConfirm.description")}
        confirmLabel={t("accounts.exportConfirm.submit")}
        busy={exportAuthMutation.isPending}
        onOpenChange={exportConfirmDialog.onOpenChange}
        onConfirm={(password) => {
          if (exportConfirmDialog.data) runExport(exportConfirmDialog.data, password);
        }}
      />

      <AuthExportDialog open={exportDialog.open} exportData={exportDialog.data} onOpenChange={exportDialog.onOpenChange} />

      {resetCreditDialog.data ? (
        <ResetCreditConfirmDialog
          open={resetCreditDialog.open}
          accountId={resetCreditDialog.data.accountId}
          summaryAvailableCount={resetCreditDialog.data.availableResetCredits}
          onOpenChange={resetCreditDialog.onOpenChange}
        />
      ) : null}

      <ConfirmDialog
        open={deleteDialog.open}
        title={t("accounts.deleteDialog.title")}
        description={t("accounts.deleteDialog.description")}
        confirmLabel={t("common.actions.delete")}
        cancelLabel={t("common.cancel")}
        onOpenChange={(open) => {
          deleteDialog.onOpenChange(open);
          if (!open) setDeleteHistory(false);
        }}
        onConfirm={() => {
          if (!deleteDialog.data) {
            return;
          }
          const accountId = deleteDialog.data;
          void deleteMutation
            .mutateAsync({ accountId, deleteHistory })
            .then(() => {
              if (selectedAccountId === accountId) closeAccount();
            })
            .catch(() => null)
            .finally(() => {
              deleteDialog.hide();
              setDeleteHistory(false);
            });
        }}
      >
        <div className="flex items-center gap-2">
          <Checkbox id="delete-history" checked={deleteHistory} onCheckedChange={(checked) => setDeleteHistory(checked === true)} />
          <label htmlFor="delete-history" className="cursor-pointer text-sm text-muted-foreground">
            {t("accounts.deleteDialog.deleteHistory")}
          </label>
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={usageResetDialog.open}
        title={t("accounts.usageResetDialog.title")}
        description={t("accounts.usageResetDialog.description")}
        confirmLabel={t("common.actions.reset")}
        cancelLabel={t("common.cancel")}
        onOpenChange={usageResetDialog.onOpenChange}
        onConfirm={() => {
          if (!usageResetDialog.data) {
            return;
          }
          void usageResetMutation
            .mutateAsync({ accountId: usageResetDialog.data })
            .then(() => checkMutation.reset())
            .catch(() => null)
            .finally(() => {
              usageResetDialog.hide();
            });
        }}
      />
    </div>
  );
}
