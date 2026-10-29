import { LogOut, Link2, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { toast } from "sonner";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { deleteKeyReportSession } from "@/features/key-reports/api";
import { KeyAdminActions } from "@/features/key-page/components/key-admin-actions";
import { KeyCostByAccountSection, KeyLifetimeSection } from "@/features/key-page/components/key-admin-sections";
import { KeyErrorsSection } from "@/features/key-page/components/key-errors-section";
import { KeyKpisSection } from "@/features/key-page/components/key-kpis-section";
import { KeyLatencySection } from "@/features/key-page/components/key-latency-section";
import { KeyLimitsSection } from "@/features/key-page/components/key-limits-section";
import { KeyPageHeader, type KeyIdentity } from "@/features/key-page/components/key-page-header";
import { KeyPeriodPicker } from "@/features/key-page/components/key-period-picker";
import { KeyPropertiesSection } from "@/features/key-page/components/key-properties-section";
import { KeyRequestsSection } from "@/features/key-page/components/key-requests-section";
import { KeyUsageSection } from "@/features/key-page/components/key-usage-section";
import { KeyUserAgentsSection } from "@/features/key-page/components/key-user-agents-section";
import { KeyPageSkeleton } from "@/features/key-page/components/section-card";
import { isNotFound, isUnauthorized, useKeyPage, useKeyPagePeriod } from "@/features/key-page/hooks/use-key-page";
import { clockOrDateLabel, dateTimeLabel } from "@/features/key-page/key-page-format";
import { ownerKeyPageLink } from "@/features/key-page/owner-link";
import { browserTimeZone } from "@/features/key-page/period";
import type { KeyPageResponse } from "@/features/key-page/schemas";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { copyToClipboard } from "@/utils/clipboard";
import { getErrorMessageOrNull } from "@/utils/errors";
import { formatAgo, parseDate } from "@/utils/formatters";

export type KeyPageProps =
  | { mode: "admin"; keyId: string; onExit?: never }
  | { mode: "owner"; keyId?: never; onExit: (message?: string) => void };

function identityOf(data: KeyPageResponse | undefined): KeyIdentity | null {
  if (!data) return null;
  const { key } = data;
  return { name: key.name, prefix: key.prefix, status: key.status, groupId: key.groupId, groupName: key.groupName };
}

function OwnerActions({ period, onExit }: { period: { from: string; to: string }; onExit: (message?: string) => void }) {
  const { t } = useTranslation();
  const [signingOut, setSigningOut] = useState(false);
  const [signOutFailed, setSignOutFailed] = useState(false);
  const signOut = async () => {
    setSigningOut(true);
    setSignOutFailed(false);
    try {
      await deleteKeyReportSession();
      onExit();
    } catch {
      setSignOutFailed(true);
      setSigningOut(false);
    }
  };
  const copyLink = async () => {
    const copied = await copyToClipboard(ownerKeyPageLink(period));
    if (copied) toast.success(t("keyPage.actions.linkCopied"));
    else toast.error(t("components.copyButton.toasts.failed"));
  };
  return (
    <>
      <Button type="button" size="sm" variant="outline" className="h-8 gap-1.5 text-xs" onClick={() => void copyLink()}>
        <Link2 className="h-3.5 w-3.5" aria-hidden="true" />
        {t("keyPage.actions.copyLink")}
      </Button>
      <Button type="button" size="sm" variant="outline" className="h-8 gap-1.5 text-xs" disabled={signingOut} onClick={() => void signOut()}>
        <LogOut className="h-3.5 w-3.5" aria-hidden="true" />
        {t("keyReports.signOut")}
      </Button>
      {signOutFailed ? (
        <div role="alert" className="w-full">
          <AlertMessage variant="error">{t("keyReports.signOutFailed")}</AlertMessage>
        </div>
      ) : null}
    </>
  );
}

/**
 * One key's page (dashboard redesign use case 4) for the administrator at
 * `/keys/:id` and for the key owner after key sign-in (R19): one period
 * picker in the URL (R24), the current limits, the period's KPIs, usage,
 * errors, latency and user agents, the recent requests and the properties.
 * Only the administrator sees the actions, the account columns, the cost by
 * account and the lifetime totals; the owner mode reads only
 * `/api/key-reports/` and never mounts the administrator navigation.
 */
export function KeyPage(props: KeyPageProps) {
  const { t } = useTranslation();
  const { mode } = props;
  const keyId = props.mode === "admin" ? props.keyId : undefined;
  const onExit = props.mode === "owner" ? props.onExit : undefined;
  const admin = mode === "admin";
  const { period, setPeriod } = useKeyPagePeriod();
  const timezone = useMemo(() => browserTimeZone(), []);
  const query = useKeyPage(mode, keyId, period, timezone);
  const directory = useDirectory(admin);
  const [now, setNow] = useState(() => Date.now());
  const [errorsRun, setErrorsRun] = useState<number | null>(null);
  const errorsRuns = useRef(0);
  const requestsRef = useRef<HTMLDivElement>(null);
  const exited = useRef(false);

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(id);
  }, []);

  const endSession = useCallback(() => {
    if (exited.current || !onExit) return;
    exited.current = true;
    onExit(t("keyReports.invalidKey"));
  }, [onExit, t]);

  const unauthorized = mode === "owner" && isUnauthorized(query.error);
  useEffect(() => {
    if (unauthorized) endSession();
  }, [unauthorized, endSession]);

  const setErrorsOnly = useCallback((on: boolean) => {
    if (on) {
      errorsRuns.current += 1;
      setErrorsRun(errorsRuns.current);
    } else {
      setErrorsRun(null);
    }
  }, []);

  const showErrors = useCallback(() => {
    setErrorsOnly(true);
    requestsRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  }, [setErrorsOnly]);

  const data = unauthorized ? undefined : query.data;
  const directoryKey = admin ? directory.data?.keys.find((entry) => entry.id === keyId) : undefined;
  const identity: KeyIdentity | null =
    identityOf(data) ??
    (directoryKey
      ? {
          name: directoryKey.name,
          prefix: directoryKey.prefix,
          status: directoryKey.status,
          groupId: directoryKey.groupId,
          groupName: directory.data?.groups.find((group) => group.id === directoryKey.groupId)?.name ?? null,
        }
      : null);

  if (admin && isNotFound(query.error)) {
    return (
      <div className="space-y-3 rounded-xl border bg-card p-6" role="alert">
        <p className="text-sm font-medium">{t("keyPage.notFound")}</p>
        <Button asChild size="sm" variant="outline">
          <Link to="/keys">{t("keyPage.backToKeys")}</Link>
        </Button>
      </div>
    );
  }

  const errorMessage = getErrorMessageOrNull(query.error) ?? t("keyPage.loadFailed");
  const empty = data !== undefined && data.summary.requests === 0;
  const lastUsed = parseDate(data?.key.lastUsedAt);
  const previousPeriod = data ? { from: data.period.previousStart, to: data.period.previousEnd } : period;

  return (
    <div className="animate-fade-in-up space-y-4" data-testid={`key-page-${mode}`}>
      <KeyPageHeader
        mode={mode}
        identity={identity}
        details={data?.key ?? null}
        now={now}
        actions={
          <>
            {admin && keyId ? <KeyAdminActions keyId={keyId} identity={identity} period={period} /> : null}
            {onExit ? <OwnerActions period={period} onExit={onExit} /> : null}
            <button
              type="button"
              onClick={() => void query.refetch()}
              disabled={query.isFetching}
              className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50"
              aria-label={t("keyPage.refresh")}
              title={t("keyPage.refresh")}
            >
              <RefreshCw className={`h-4 w-4${query.isFetching ? " animate-spin" : ""}`} aria-hidden="true" />
            </button>
          </>
        }
      />

      <KeyPeriodPicker period={period} timezone={timezone} onChange={setPeriod} />

      {query.isError && !unauthorized ? (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border bg-card p-3" role="alert">
          <AlertMessage variant="error">
            {data ? t("keyPage.staleSince", { time: clockOrDateLabel(data.generatedAt), error: errorMessage }) : errorMessage}
          </AlertMessage>
          <Button type="button" variant="outline" size="sm" onClick={() => void query.refetch()} disabled={query.isFetching}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}

      {data && !data.rollupsUsable ? <AlertMessage variant="warning">{t("keyPage.rollupsCatchingUp")}</AlertMessage> : null}
      {data?.detailsFrom ? (
        <AlertMessage variant="warning">{t("keyPage.detailsFrom", { date: dateTimeLabel(data.detailsFrom) })}</AlertMessage>
      ) : null}

      {!data ? (
        query.isError ? null : <KeyPageSkeleton />
      ) : (
        <>
          <KeyLimitsSection limits={data.limits} pool={data.pool} group={data.group} now={now} />
          {empty ? (
            <div className="rounded-xl border bg-card p-4 text-sm" data-testid="key-page-empty">
              <p className="font-medium">{t("keyPage.empty.noRequests")}</p>
              <p className="mt-1 text-xs text-muted-foreground">
                {lastUsed
                  ? t("keyPage.empty.lastUsed", { time: dateTimeLabel(data.key.lastUsedAt), ago: formatAgo(now - lastUsed.getTime()) })
                  : t("keyPage.empty.neverUsed")}
              </p>
            </div>
          ) : (
            <>
              <KeyKpisSection summary={data.summary} previousPeriod={previousPeriod} />
              <KeyUsageSection daily={data.daily} byModel={data.byModel} />
              <KeyErrorsSection
                mode={mode}
                keyId={keyId}
                period={period}
                rows={data.errorsByCode}
                daily={data.daily}
                onShowErrors={showErrors}
              />
              <div className="grid gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
                <KeyLatencySection daily={data.daily} summary={data.summary} />
                <KeyUserAgentsSection rows={data.userAgents} />
              </div>
            </>
          )}
          <div ref={requestsRef} className="scroll-mt-4">
            <KeyRequestsSection
              mode={mode}
              keyId={keyId}
              firstPage={data.recentRequests}
              firstCursor={data.nextCursor}
              errorsRun={errorsRun}
              onErrorsOnlyChange={setErrorsOnly}
              onUnauthorized={endSession}
              directory={directory.data}
            />
          </div>
          <KeyPropertiesSection mode={mode} details={data.key} directory={directory.data} />
          {admin ? (
            <>
              {!empty ? <KeyCostByAccountSection rows={data.byAccount ?? []} directory={directory.data} /> : null}
              {data.lifetime ? <KeyLifetimeSection lifetime={data.lifetime} /> : null}
            </>
          ) : null}
        </>
      )}
    </div>
  );
}
