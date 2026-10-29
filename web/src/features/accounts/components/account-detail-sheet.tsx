import { Check, Pencil, X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { isEmailLabel } from "@/components/blur-email";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { AccountGroupsEditor } from "@/features/accounts/components/account-groups-editor";
import { AccountActions } from "@/features/accounts/components/account-actions";
import { AccountTokenInfo } from "@/features/accounts/components/account-token-info";
import { AccountUsagePanel } from "@/features/accounts/components/account-usage-panel";
import type { CheckedResetCredits } from "@/features/accounts/hooks/use-accounts";
import type { AccountRoutingPolicy, AccountSummary } from "@/features/accounts/schemas";
import { useAccountTrends } from "@/features/accounts/hooks/use-accounts";
import { formatCompactAccountId } from "@/utils/account-identifiers";
import { normalizeStatus } from "@/utils/account-status";
import { formatSlug } from "@/utils/formatters";

export type AccountDetailSheetProps = {
  /** The account shown; null closes the sheet. */
  account: AccountSummary | null;
  onClose: () => void;
  busy: boolean;
  onPause: (accountId: string) => void;
  onResume: (accountId: string) => void;
  onProbe: (accountId: string) => void;
  onResetUsage: (accountId: string) => void;
  onSetAlias: (accountId: string, alias: string | null) => Promise<unknown>;
  onDelete: (accountId: string) => void;
  onReauth: (accountId: string) => void;
  onExportAuth: (accountId: string) => void;
  onResetCredit: (accountId: string) => void;
  showResetCreditExpiryBadge?: boolean;
  onLimitWarmupChange: (accountId: string, enabled: boolean) => void;
  onRoutingPolicyChange: (accountId: string, routingPolicy: AccountRoutingPolicy) => void;
  onSecurityWorkAuthorizedChange: (accountId: string, enabled: boolean) => void;
  checkedResetCredits: CheckedResetCredits | null;
  checkingResetCredits: boolean;
  onCheckResetCredits: (accountId: string) => void;
};

/**
 * One account's details in a sheet over the accounts table (dashboard
 * redesign use case 8): alias editing, quota windows with resets and the
 * 7-day quota chart (fetched only while open, every 5 minutes), usage of the
 * last 7 days and lifetime, reset credits with Check now, tokens, groups and
 * the routing, warm-up and trusted-access settings with the actions. Opening
 * it never calls a provider (R42).
 */
export function AccountDetailSheet({
  account,
  onClose,
  busy,
  onPause,
  onResume,
  onProbe,
  onResetUsage,
  onSetAlias,
  onDelete,
  onReauth,
  onExportAuth,
  onResetCredit,
  showResetCreditExpiryBadge = true,
  onLimitWarmupChange,
  onRoutingPolicyChange,
  onSecurityWorkAuthorizedChange,
  checkedResetCredits,
  checkingResetCredits,
  onCheckResetCredits,
}: AccountDetailSheetProps) {
  return (
    <Sheet open={account !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent side="right" className="w-full gap-0 overflow-y-auto sm:max-w-xl" data-testid="account-detail-sheet">
        {account ? (
          <AccountDetailContent
            account={account}
            busy={busy}
            onPause={onPause}
            onResume={onResume}
            onProbe={onProbe}
            onResetUsage={onResetUsage}
            onSetAlias={onSetAlias}
            onDelete={onDelete}
            onReauth={onReauth}
            onExportAuth={onExportAuth}
            onResetCredit={onResetCredit}
            showResetCreditExpiryBadge={showResetCreditExpiryBadge}
            onLimitWarmupChange={onLimitWarmupChange}
            onRoutingPolicyChange={onRoutingPolicyChange}
            onSecurityWorkAuthorizedChange={onSecurityWorkAuthorizedChange}
            checkedResetCredits={checkedResetCredits}
            checkingResetCredits={checkingResetCredits}
            onCheckResetCredits={onCheckResetCredits}
          />
        ) : null}
      </SheetContent>
    </Sheet>
  );
}

function AccountDetailContent({
  account,
  busy,
  onPause,
  onResume,
  onProbe,
  onResetUsage,
  onSetAlias,
  onDelete,
  onReauth,
  onExportAuth,
  onResetCredit,
  showResetCreditExpiryBadge,
  onLimitWarmupChange,
  onRoutingPolicyChange,
  onSecurityWorkAuthorizedChange,
  checkedResetCredits,
  checkingResetCredits,
  onCheckResetCredits,
}: Omit<AccountDetailSheetProps, "account" | "onClose"> & { account: AccountSummary }) {
  const { t } = useTranslation();
  const { data: trends } = useAccountTrends(account.accountId);
  const blurred = usePrivacyStore((s) => s.blurred);
  const showAccountId = account.isEmailDuplicate === true;

  const aliasLabel = account.alias?.trim() ?? "";
  const localLabel = aliasLabel || account.displayName || account.email;
  const labelIsEmail = !aliasLabel && isEmailLabel(localLabel, account.email);
  const compactId = formatCompactAccountId(account.accountId);
  const emailSubtitle =
    aliasLabel || (account.displayName && account.displayName !== account.email)
      ? account.email
      : null;
  const idSuffix = showAccountId ? ` (${compactId})` : "";
  const workspaceLabel = account.chatgptAccountId || account.workspaceLabel || account.workspaceId || t("accounts.detail.unknownWorkspace");
  const seatLabel = account.seatType ? ` | ${formatSlug(account.seatType)}` : "";
  const operatorRecoveryAction =
    account.status === "reauth_required" || account.status === "deactivated";
  const inactive = account.status === "paused" || operatorRecoveryAction;

  return (
    <div key={account.accountId} className="min-w-0 space-y-4 p-4 sm:p-5">
      <SheetHeader className="gap-0.5 p-0 pr-8">
        <AccountNameField
          key={account.accountId}
          accountId={account.accountId}
          alias={account.alias ?? null}
          localLabel={localLabel}
          labelIsEmail={labelIsEmail}
          idSuffix={emailSubtitle ? "" : idSuffix}
          blurred={blurred}
          busy={busy}
          onSetAlias={onSetAlias}
        />
        {emailSubtitle ? (
          <p
            className="text-xs text-muted-foreground"
            title={showAccountId ? t("accounts.detail.accountIdTitle", { accountId: account.accountId }) : undefined}
          >
            <span className={blurred ? "privacy-blur" : ""}>{emailSubtitle}</span>
            {showAccountId ? ` | ID ${compactId}` : ""}
          </p>
        ) : null}
        <SheetDescription className="flex flex-wrap items-center gap-2 text-xs">
          <span>
            {workspaceLabel} | {formatSlug(account.planType)}{seatLabel}
          </span>
          <StatusBadge status={normalizeStatus(account.status)} />
        </SheetDescription>
      </SheetHeader>

      <AccountUsagePanel
        account={account}
        trends={trends}
        checkedResetCredits={checkedResetCredits}
        checking={checkingResetCredits}
        checkDisabled={busy || inactive}
        onCheckNow={onCheckResetCredits}
        resetDisabled={busy || inactive}
        onReset={onResetUsage}
      />
      <AccountTokenInfo account={account} />
      <AccountGroupsEditor
        key={`${account.accountId}:${account.groups.join(",")}`}
        accountId={account.accountId}
        currentGroupIds={account.groups}
        busy={busy}
      />
      <AccountActions
        account={account}
        busy={busy}
        onPause={onPause}
        onResume={onResume}
        onProbe={onProbe}
        onDelete={onDelete}
        onReauth={() => onReauth(account.accountId)}
        onExportAuth={onExportAuth}
        onResetCredit={onResetCredit}
        showResetCreditExpiryBadge={showResetCreditExpiryBadge}
        onLimitWarmupChange={onLimitWarmupChange}
        onRoutingPolicyChange={onRoutingPolicyChange}
        onSecurityWorkAuthorizedChange={onSecurityWorkAuthorizedChange}
      />
    </div>
  );
}

type AccountNameFieldProps = {
  accountId: string;
  alias: string | null;
  localLabel: string;
  labelIsEmail: boolean;
  idSuffix: string;
  blurred: boolean;
  busy: boolean;
  onSetAlias: (accountId: string, alias: string | null) => Promise<unknown>;
};

function AccountNameField({
  accountId,
  alias,
  localLabel,
  labelIsEmail,
  idSuffix,
  blurred,
  busy,
  onSetAlias,
}: AccountNameFieldProps) {
  const { t } = useTranslation();
  const [isEditing, setIsEditing] = useState(false);
  const [aliasDraft, setAliasDraft] = useState(alias ?? "");

  const handleSave = async () => {
    const trimmed = aliasDraft.trim();
    await onSetAlias(accountId, trimmed === "" ? null : trimmed);
    setIsEditing(false);
  };

  const handleCancel = () => {
    setAliasDraft(alias ?? "");
    setIsEditing(false);
  };

  if (isEditing) {
    return (
      <div className="space-y-1.5">
        <div className="flex items-center gap-1.5">
          <Input
            id="account-alias"
            aria-label={t("accounts.detail.aliasLabel")}
            className="h-8 text-sm"
            maxLength={255}
            placeholder={t("accounts.detail.aliasPlaceholder")}
            value={aliasDraft}
            autoFocus
            disabled={busy}
            onChange={(event) => setAliasDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                void handleSave();
              } else if (event.key === "Escape") {
                event.preventDefault();
                handleCancel();
              }
            }}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={t("accounts.detail.saveAlias")}
            disabled={busy}
            onClick={() => void handleSave()}
          >
            <Check className="size-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={t("common.cancel")}
            onClick={handleCancel}
          >
            <X className="size-4" />
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("accounts.detail.aliasHelp")}
        </p>
      </div>
    );
  }

  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <SheetTitle className="min-w-0 truncate text-base font-semibold">
        {labelIsEmail ? (
          <>
            <span className={cn(blurred && "privacy-blur")}>{localLabel}</span>
            {idSuffix}
          </>
        ) : (
          <>
            {localLabel}
            {idSuffix}
          </>
        )}
      </SheetTitle>
      <Button
        type="button"
        variant="ghost"
        size="icon-xs"
        aria-label={t("accounts.detail.editAlias")}
        title={t("accounts.detail.aliasHelp")}
        disabled={busy}
        onClick={() => {
          setAliasDraft(alias ?? "");
          setIsEditing(true);
        }}
      >
        <Pencil className="size-3.5" />
      </Button>
    </div>
  );
}
