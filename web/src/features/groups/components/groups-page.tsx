import { Plus, RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { GroupDetailSheet } from "@/features/groups/components/group-detail-sheet";
import { GroupsTable } from "@/features/groups/components/groups-table";
import { PoolRow } from "@/features/groups/components/pool-row";
import { PoolRunwayCard } from "@/features/groups/components/pool-runway-card";
import { useGroupsSummary, usePoolRunway } from "@/features/groups/hooks/use-groups-summary";
import { clockLabel } from "@/features/overview/overview-format";
import { getErrorMessageOrNull } from "@/utils/errors";

/** The value of `?selected=` that opens the create sheet. */
const NEW_GROUP = "new";

function GroupsSkeleton() {
  return (
    <div className="space-y-4" data-testid="groups-skeleton">
      <Skeleton className="h-20 w-full rounded-xl" />
      <div className="space-y-2 rounded-xl border p-4">
        {Array.from({ length: 6 }, (_, index) => (
          <Skeleton key={index} className="h-8 w-full" />
        ))}
      </div>
    </div>
  );
}

/**
 * The Groups page (dashboard redesign use case 7): toolbar, the pool row and
 * its runway card, the sortable groups table and the detail sheet of the
 * group named by `?selected=`. Its only periodic request is
 * GET /api/groups/summary every 60 s (R40).
 */
export function GroupsPage() {
  const { t } = useTranslation();
  const summary = useGroupsSummary();
  const runway = usePoolRunway();
  const [params, setParams] = useSearchParams();
  const [search, setSearch] = useState("");
  const data = summary.data;
  const selected = params.get("selected");
  const select = (id: string | null) => {
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (id) next.set("selected", id);
        else next.delete("selected");
        return next;
      },
      { replace: id === null },
    );
  };
  const rows = useMemo(() => {
    const needle = search.trim().toLowerCase();
    const groups = data?.groups ?? [];
    return needle ? groups.filter((row) => row.id !== null && row.name.toLowerCase().includes(needle)) : groups;
  }, [data, search]);
  const selectedRow = selected && selected !== NEW_GROUP ? data?.groups.find((row) => row.id === selected) : undefined;
  const hasGroups = Boolean(data?.groups.some((row) => row.id !== null));
  const errorMessage = getErrorMessageOrNull(summary.error) ?? t("groups.loadFailed");

  return (
    <div className="animate-fade-in-up space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("groups.title")}</h1>
          <p className="mt-1 text-xs text-muted-foreground">{t("groups.subtitle", { zone: summary.timezone })}</p>
        </div>
        <div className="flex items-center gap-2">
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t("groups.search")}
            aria-label={t("groups.search")}
            className="h-8 w-56 text-xs"
          />
          <Button type="button" size="sm" className="h-8 gap-1.5 text-xs" onClick={() => select(NEW_GROUP)}>
            <Plus className="h-3.5 w-3.5" />
            {t("groups.create")}
          </Button>
          <button
            type="button"
            onClick={() => void summary.refetch()}
            disabled={summary.isFetching}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50"
            aria-label={t("groups.refresh")}
            title={t("groups.refresh")}
          >
            <RefreshCw className={`h-4 w-4${summary.isFetching ? " animate-spin" : ""}`} aria-hidden="true" />
          </button>
        </div>
      </div>

      {summary.isError ? (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border bg-card p-3" role="alert">
          <AlertMessage variant="error">
            {data ? t("groups.staleSince", { time: clockLabel(data.generatedAt), error: errorMessage }) : errorMessage}
          </AlertMessage>
          <Button type="button" variant="outline" size="sm" onClick={() => void summary.refetch()} disabled={summary.isFetching}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}

      {data && !data.shell.rollupsUsable ? <AlertMessage variant="warning">{t("groups.rollupsCatchingUp")}</AlertMessage> : null}

      {!data ? (
        summary.isError ? null : <GroupsSkeleton />
      ) : (
        <>
          <div className="grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(18rem,1fr)]">
            <PoolRow pool={data.pool} />
            <PoolRunwayCard pace={runway.data?.weeklyCreditPace} loading={runway.isLoading} />
          </div>
          {!hasGroups ? (
            <div className="flex flex-wrap items-center gap-3 rounded-xl border bg-card p-4 text-sm">
              <span>{t("groups.empty")}</span>
              <Button type="button" size="sm" variant="outline" onClick={() => select(NEW_GROUP)}>
                {t("groups.create")}
              </Button>
            </div>
          ) : null}
          <GroupsTable rows={rows} onOpen={(id) => select(id)} />
        </>
      )}

      {data && selected === NEW_GROUP ? (
        <GroupDetailSheet key="new" row={null} accounts={data.accounts} onClose={() => select(null)} onCreated={(id) => select(id)} />
      ) : null}
      {data && selectedRow ? (
        <GroupDetailSheet key={selectedRow.id} row={selectedRow} accounts={data.accounts} onClose={() => select(null)} onCreated={(id) => select(id)} />
      ) : null}
    </div>
  );
}
