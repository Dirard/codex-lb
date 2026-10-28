import { RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { AlertsPanel } from "@/features/overview/components/alerts-panel";
import { AttentionKeysTable } from "@/features/overview/components/attention-keys-table";
import { GroupHealthTable } from "@/features/overview/components/group-health-table";
import { useOverviewActions } from "@/features/overview/components/overview-actions";
import { PoolSummaryStrip } from "@/features/overview/components/pool-summary-strip";
import { useOverview } from "@/features/overview/hooks/use-overview";
import { agoLabel, clockLabel } from "@/features/overview/overview-format";
import { getErrorMessageOrNull } from "@/utils/errors";

function OverviewSkeleton() {
  return (
    <div className="space-y-4" data-testid="overview-skeleton">
      <Skeleton className="h-12 w-full rounded-xl" />
      <Skeleton className="h-28 w-full rounded-xl" />
      <div className="space-y-2 rounded-xl border p-4">
        {Array.from({ length: 6 }, (_, index) => (
          <Skeleton key={index} className="h-8 w-full" />
        ))}
      </div>
    </div>
  );
}

/**
 * The overview by account groups (dashboard redesign use case 2): alerts,
 * the pool strip, the groups table and the keys needing attention, in that
 * order, without a period picker; every figure carries its fixed period.
 * Its only periodic request is GET /api/overview every 30 s (R7, R8).
 */
export function OverviewPage() {
  const { t } = useTranslation();
  const overview = useOverview();
  const { perform, dialogs } = useOverviewActions();
  const data = overview.data;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 10_000);
    return () => clearInterval(id);
  }, []);
  const errorMessage = getErrorMessageOrNull(overview.error) ?? t("overview.loadFailed");
  const shell = data?.shell;
  const synced = agoLabel(shell?.lastSyncAt, now);
  const folded = agoLabel(shell?.usageFoldedAt, now);
  const trendsKnown = Boolean(data?.shell.rollupsUsable && data.pool.trend7d);

  return (
    <div className="animate-fade-in-up space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("overview.title")}</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("overview.timezone", { zone: overview.timezone })}
            {shell ? (
              <>
                {" · "}
                {synced ? t("overview.quotaSyncedAgo", { ago: synced }) : t("overview.quotaNeverSynced")}
                {" · "}
                {folded ? t("overview.usageFoldedAgo", { ago: folded }) : t("overview.usageNeverFolded")}
              </>
            ) : null}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button asChild variant="ghost" size="sm" className="h-8 text-xs">
            <Link to="/requests">{t("overview.links.requests")}</Link>
          </Button>
          <Button asChild variant="ghost" size="sm" className="h-8 text-xs">
            <Link to="/reports">{t("overview.links.reports")}</Link>
          </Button>
          <button
            type="button"
            onClick={() => void overview.refetch()}
            disabled={overview.isFetching}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50"
            aria-label={t("overview.refresh")}
            title={t("overview.refresh")}
          >
            <RefreshCw className={`h-4 w-4${overview.isFetching ? " animate-spin" : ""}`} aria-hidden="true" />
          </button>
        </div>
      </div>

      {overview.isError ? (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border bg-card p-3" role="alert">
          <AlertMessage variant="error">
            {data
              ? t("overview.staleSince", { time: clockLabel(data.generatedAt), error: errorMessage })
              : errorMessage}
          </AlertMessage>
          <Button type="button" variant="outline" size="sm" onClick={() => void overview.refetch()} disabled={overview.isFetching}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}

      {data && !data.shell.rollupsUsable ? (
        <AlertMessage variant="warning">{t("overview.rollupsCatchingUp")}</AlertMessage>
      ) : null}

      {!data ? (
        overview.isError ? null : <OverviewSkeleton />
      ) : (
        <>
          <AlertsPanel alerts={data.alerts} onAction={perform} />
          {data.pool.accounts.ok + data.pool.accounts.low + data.pool.accounts.limited + data.pool.accounts.reauth +
            data.pool.accounts.paused + data.pool.accounts.off + data.pool.externalSources === 0 ? (
            <div className="rounded-xl border bg-card p-4 text-sm">
              {t("overview.empty.noAccounts")}{" "}
              <Link className="font-medium text-primary hover:underline" to="/accounts?add=1">
                {t("overview.empty.addAccount")}
              </Link>
            </div>
          ) : null}
          <PoolSummaryStrip pool={data.pool} trendsKnown={trendsKnown} />
          <GroupHealthTable rows={data.groups} trendsKnown={trendsKnown} onAction={perform} />
          {data.groups.every((row) => row.keys.total === 0) ? (
            <div className="rounded-xl border bg-card p-4 text-sm">
              {t("overview.empty.noKeys")}{" "}
              <Link className="font-medium text-primary hover:underline" to="/keys">
                {t("overview.empty.createKey")}
              </Link>
            </div>
          ) : null}
          <AttentionKeysTable keys={data.attentionKeys} total={data.attentionKeysTotal} onAction={perform} />
        </>
      )}
      {dialogs}
    </div>
  );
}
