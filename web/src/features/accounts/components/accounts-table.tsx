import { Activity, Download, MoreHorizontal, Pause, Play, RefreshCw, RotateCcw, ShieldCheck, Trash2 } from "lucide-react";
import type { MouseEvent, ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { isEmailLabel } from "@/components/blur-email";
import { MiniQuotaBar } from "@/components/mini-quota-bar";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { SubscriptionRemaining } from "@/features/accounts/components/subscription-remaining";
import type { AccountRoutingPolicy, AccountSummary } from "@/features/accounts/schemas";
import { accountLabel } from "@/features/accounts/sorting";
import { useDateDisplayFormatStore } from "@/hooks/use-date-format";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { cn } from "@/lib/utils";
import { formatCompactAccountId } from "@/utils/account-identifiers";
import { normalizeStatus } from "@/utils/account-status";
import { formatNumber, formatPercentNullable, formatQuotaResetLabel, formatSingleUnitRemaining, formatSlug } from "@/utils/formatters";

export type AccountsTableProps = {
  accounts: AccountSummary[];
  /** Group names by id, from the directory. */
  groupNames: Map<string, string>;
  showMonthly: boolean;
  busy: boolean;
  selectedAccountId: string | null;
  onOpen: (accountId: string) => void;
  onRoutingPolicyChange: (accountId: string, routingPolicy: AccountRoutingPolicy) => void;
  onPause: (accountId: string) => void;
  onResume: (accountId: string) => void;
  onReauth: (accountId: string) => void;
  onProbe: (accountId: string) => void;
  onExportAuth: (accountId: string) => void;
  onRedeemResetCredit: (accountId: string) => void;
  onDelete: (accountId: string) => void;
};

/** Accounts the operator must recover before they can route again. */
function needsRecovery(account: AccountSummary): boolean {
  return account.status === "reauth_required" || account.status === "deactivated";
}

/** Stops a click on a control from opening the row's detail sheet. */
function stop(event: MouseEvent) {
  event.stopPropagation();
}

/** A window's remaining percent with its reset; `—` when the window is absent. */
function QuotaCell({ label, percent, resetAt, windowMinutes }: {
  label: string;
  percent: number | null | undefined;
  resetAt: string | null | undefined;
  windowMinutes: number | null | undefined;
}) {
  const { t } = useTranslation();
  if (percent == null && resetAt == null && windowMinutes == null) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <div className="min-w-[6.5rem] space-y-1">
      <div className="flex items-center gap-2">
        <span className="w-11 shrink-0 tabular-nums text-xs font-medium">{formatPercentNullable(percent ?? null, 0)}</span>
        <MiniQuotaBar percent={percent ?? null} testId={`quota-${label}`} aria-label={t("accounts.listItem.quotaRemainingAria", { label })} />
      </div>
      <p className="text-[11px] text-muted-foreground">
        {resetAt ? t("accounts.table.resetsIn", { label: formatQuotaResetLabel(resetAt) }) : t("accounts.table.noReset")}
      </p>
    </div>
  );
}

/** The window that blocks a limited account, shown as the reason. */
function statusReason(account: AccountSummary, t: ReturnType<typeof useTranslation>["t"]): string | null {
  switch (account.status) {
    case "reauth_required":
      return t("accounts.table.reasonReauth");
    case "paused":
      return t("accounts.table.reasonPaused");
    case "deactivated":
      return account.deactivationReason ? formatSlug(account.deactivationReason) : null;
    case "quota_exceeded":
    case "rate_limited": {
      const blocked = [
        [account.usage?.primaryRemainingPercent, account.resetAtPrimary],
        [account.usage?.secondaryRemainingPercent, account.resetAtSecondary],
        [account.usage?.monthlyRemainingPercent, account.resetAtMonthly],
      ].filter(([remaining, reset]) => remaining != null && Number(remaining) <= 0 && reset) as [number, string][];
      const until = blocked.map(([, reset]) => reset).sort()[0];
      return until ? t("accounts.table.reasonUntil", { label: formatQuotaResetLabel(until) }) : null;
    }
  }
  return null;
}

function CreditsCell({ account }: { account: AccountSummary }) {
  const { t } = useTranslation();
  if (account.creditsUnlimited) {
    return <span>{t("accounts.table.unlimited")}</span>;
  }
  if (account.creditsBalance != null) {
    return <span className="tabular-nums">{formatNumber(account.creditsBalance)}</span>;
  }
  return <span className="text-muted-foreground">—</span>;
}

function ResetCreditsCell({ account }: { account: AccountSummary }) {
  if (account.availableResetCredits == null) {
    return <span className="text-muted-foreground">—</span>;
  }
  const expiry = account.availableResetCredits > 0 && account.resetCreditNearestExpiresAt
    ? formatSingleUnitRemaining(account.resetCreditNearestExpiresAt)
    : null;
  return (
    <span className="tabular-nums">
      {account.availableResetCredits}
      {expiry ? (
        <span className={cn("ml-1 text-[11px]", expiry.expiringSoon ? "text-destructive" : "text-muted-foreground")}>
          ({expiry.label})
        </span>
      ) : null}
    </span>
  );
}

function Header({ children, className }: { children?: ReactNode; className?: string }) {
  return <th className={cn("px-3 py-2 text-left font-medium whitespace-nowrap", className)}>{children}</th>;
}

/**
 * Every account in one table (dashboard redesign use case 8): label and
 * e-mail, plan, status with its reason, groups, window remaining with resets,
 * purchased and reset credits, warm-up, the inline priority select, inline
 * Re-login or Pause/Resume, and a menu with Probe, Export auth, Redeem reset
 * credit and Delete (R45). Opening a row shows its detail sheet.
 */
export function AccountsTable({
  accounts,
  groupNames,
  showMonthly,
  busy,
  selectedAccountId,
  onOpen,
  onRoutingPolicyChange,
  onPause,
  onResume,
  onReauth,
  onProbe,
  onExportAuth,
  onRedeemResetCredit,
  onDelete,
}: AccountsTableProps) {
  const { t } = useTranslation();
  const blurred = usePrivacyStore((s) => s.blurred);
  const dateDisplayFormat = useDateDisplayFormatStore((s) => s.dateDisplayFormat);

  return (
    <div className="overflow-x-auto rounded-xl border bg-card" data-testid="accounts-table">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b text-[11px] uppercase tracking-wider text-muted-foreground">
            <Header>{t("accounts.table.account")}</Header>
            <Header>{t("accounts.table.plan")}</Header>
            <Header>{t("accounts.table.status")}</Header>
            <Header>{t("accounts.table.groups")}</Header>
            <Header>5h</Header>
            <Header>{t("common.quota.weekly")}</Header>
            {showMonthly ? <Header>{t("common.quota.monthly")}</Header> : null}
            <Header className="text-right">{t("accounts.table.credits")}</Header>
            <Header className="text-right">{t("accounts.table.resetCredits")}</Header>
            <Header>{t("accounts.table.warmup")}</Header>
            <Header>{t("accounts.table.priority")}</Header>
            <Header>
              <span className="sr-only">{t("accounts.table.actions")}</span>
            </Header>
          </tr>
        </thead>
        <tbody>
          {accounts.map((account) => {
            const label = accountLabel(account);
            const labelIsEmail = isEmailLabel(label, account.email);
            const recovery = needsRecovery(account);
            const paused = account.status === "paused";
            const reason = statusReason(account, t);
            const resetCredits = account.availableResetCredits ?? 0;
            const emailSubtitle = label !== account.email ? account.email : null;
            const compactId = account.isEmailDuplicate ? formatCompactAccountId(account.accountId) : null;
            return (
              <tr
                key={account.accountId}
                data-testid="accounts-table-row"
                data-account-id={account.accountId}
                aria-selected={account.accountId === selectedAccountId}
                className={cn(
                  "cursor-pointer border-b align-top last:border-0 hover:bg-muted/40",
                  account.accountId === selectedAccountId && "bg-primary/5",
                )}
                onClick={() => onOpen(account.accountId)}
              >
                <td className="max-w-[16rem] px-3 py-2">
                  <button
                    type="button"
                    className="block max-w-full truncate text-left font-medium hover:underline"
                    onClick={(event) => {
                      stop(event);
                      onOpen(account.accountId);
                    }}
                  >
                    <span className={cn(labelIsEmail && blurred && "privacy-blur")}>{label}</span>
                  </button>
                  {emailSubtitle || compactId ? (
                    <p
                      className="truncate text-xs text-muted-foreground"
                      title={compactId ? t("accounts.detail.accountIdTitle", { accountId: account.accountId }) : undefined}
                    >
                      {emailSubtitle ? <span className={cn(blurred && "privacy-blur")}>{emailSubtitle}</span> : null}
                      {compactId ? `${emailSubtitle ? " | " : ""}ID ${compactId}` : null}
                    </p>
                  ) : null}
                </td>
                <td className="px-3 py-2 whitespace-nowrap">
                  <span className="flex items-center gap-1">
                    {formatSlug(account.planType || "—")}
                    {account.securityWorkAuthorized ? (
                      <ShieldCheck className="h-3.5 w-3.5 text-emerald-600" aria-label={t("accounts.actions.trustedAccess")} />
                    ) : null}
                  </span>
                  <span className="block text-[11px] text-muted-foreground">
                    <SubscriptionRemaining activeUntil={account.subscriptionActiveUntil} displayFormat={dateDisplayFormat} />
                  </span>
                </td>
                <td className="px-3 py-2">
                  <StatusBadge status={normalizeStatus(account.status)} />
                  {reason ? <p className="mt-0.5 max-w-[10rem] text-[11px] text-muted-foreground">{reason}</p> : null}
                </td>
                <td className="px-3 py-2">
                  {account.groups.length === 0 ? (
                    <span className="text-muted-foreground">—</span>
                  ) : (
                    <div className="flex max-w-[12rem] flex-wrap gap-1">
                      {account.groups.map((groupId) => (
                        <span key={groupId} className="rounded-full border px-1.5 py-0.5 text-[11px]">
                          {groupNames.get(groupId) ?? groupId}
                        </span>
                      ))}
                    </div>
                  )}
                </td>
                <td className="px-3 py-2">
                  <QuotaCell label="5h" percent={account.usage?.primaryRemainingPercent} resetAt={account.resetAtPrimary}
                    windowMinutes={account.windowMinutesPrimary} />
                </td>
                <td className="px-3 py-2">
                  <QuotaCell label="weekly" percent={account.usage?.secondaryRemainingPercent} resetAt={account.resetAtSecondary}
                    windowMinutes={account.windowMinutesSecondary} />
                </td>
                {showMonthly ? (
                  <td className="px-3 py-2">
                    <QuotaCell label="monthly" percent={account.usage?.monthlyRemainingPercent} resetAt={account.resetAtMonthly}
                      windowMinutes={account.windowMinutesMonthly} />
                  </td>
                ) : null}
                <td className="px-3 py-2 text-right">
                  <CreditsCell account={account} />
                </td>
                <td className="px-3 py-2 text-right">
                  <ResetCreditsCell account={account} />
                </td>
                <td className="px-3 py-2 text-xs whitespace-nowrap">
                  {account.limitWarmupEnabled ? t("accounts.table.on") : <span className="text-muted-foreground">{t("accounts.table.off")}</span>}
                </td>
                <td className="px-3 py-2" onClick={stop}>
                  {recovery ? (
                    <span className="text-muted-foreground">—</span>
                  ) : (
                    <Select
                      value={account.routingPolicy ?? "normal"}
                      onValueChange={(value) => onRoutingPolicyChange(account.accountId, value as AccountRoutingPolicy)}
                      disabled={busy}
                    >
                      <SelectTrigger
                        size="sm"
                        className="h-7 w-[7.5rem] min-w-0 text-xs"
                        aria-label={t("accounts.table.priorityAria", { label })}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="burn_first">{t("common.routingPolicies.burnFirst")}</SelectItem>
                        <SelectItem value="normal">{t("common.routingPolicies.normal")}</SelectItem>
                        <SelectItem value="preserve">{t("common.routingPolicies.preserve")}</SelectItem>
                      </SelectContent>
                    </Select>
                  )}
                </td>
                <td className="px-3 py-2" onClick={stop}>
                  <div className="flex items-center justify-end gap-1">
                    {recovery ? (
                      <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2 text-xs" disabled={busy}
                        onClick={() => onReauth(account.accountId)}>
                        <RefreshCw className="h-3.5 w-3.5" />
                        {t("accounts.table.relogin")}
                      </Button>
                    ) : paused ? (
                      <Button type="button" size="sm" className="h-7 gap-1 px-2 text-xs" disabled={busy}
                        onClick={() => onResume(account.accountId)}>
                        <Play className="h-3.5 w-3.5" />
                        {t("common.actions.resume")}
                      </Button>
                    ) : (
                      <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2 text-xs" disabled={busy}
                        onClick={() => onPause(account.accountId)}>
                        <Pause className="h-3.5 w-3.5" />
                        {t("common.actions.pause")}
                      </Button>
                    )}
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button type="button" variant="ghost" size="icon-sm" aria-label={t("accounts.table.moreActions", { label })}>
                          <MoreHorizontal className="h-4 w-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem disabled={busy || paused || recovery} onSelect={() => onProbe(account.accountId)}>
                          <Activity className="h-3.5 w-3.5" />
                          {t("accounts.actions.forceProbe")}
                        </DropdownMenuItem>
                        <DropdownMenuItem disabled={busy} onSelect={() => onExportAuth(account.accountId)}>
                          <Download className="h-3.5 w-3.5" />
                          {t("accounts.table.exportAuth")}
                        </DropdownMenuItem>
                        {resetCredits > 0 ? (
                          <DropdownMenuItem disabled={busy || paused || recovery} onSelect={() => onRedeemResetCredit(account.accountId)}>
                            <RotateCcw className="h-3.5 w-3.5" />
                            {t("accounts.table.redeemResetCredit", { count: resetCredits })}
                          </DropdownMenuItem>
                        ) : null}
                        {!recovery ? (
                          <DropdownMenuItem disabled={busy} onSelect={() => onReauth(account.accountId)}>
                            <RefreshCw className="h-3.5 w-3.5" />
                            {t("accounts.table.relogin")}
                          </DropdownMenuItem>
                        ) : null}
                        <DropdownMenuSeparator />
                        <DropdownMenuItem variant="destructive" disabled={busy} onSelect={() => onDelete(account.accountId)}>
                          <Trash2 className="h-3.5 w-3.5" />
                          {t("common.actions.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
