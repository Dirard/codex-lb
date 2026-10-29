import { ChevronDown, ChevronRight, KeyRound, Users } from "lucide-react";
import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";

import { SparklineChart } from "@/components/sparkline-chart";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { GroupRowDetail } from "@/features/overview/components/group-row-detail";
import { HealthBar } from "@/features/overview/components/health-bar";
import type { OverviewAction } from "@/features/overview/components/overview-actions";
import { clockLabel, percentLabel, trendPoints, untilLabel, usdLabel } from "@/features/overview/overview-format";
import type { GroupRow, WindowHealth } from "@/features/overview/schemas";
import { cn } from "@/lib/utils";

const UNGROUPED = "__ungrouped__";

function rowKey(row: GroupRow): string {
  return row.id ?? UNGROUPED;
}

function WindowColumn({ health }: { health: WindowHealth }) {
  const reset = untilLabel(health.nextResetAt);
  return (
    <div className="tabular-nums">
      <span className="font-medium">{percentLabel(health.remainingPercent)}</span>
      {reset ? <span className="block text-[11px] text-muted-foreground">{reset}</span> : null}
    </div>
  );
}

/** The earlier exhaustion of the two windows, shown only when it falls before the reset. */
function exhaustionOf(row: GroupRow): string | null {
  const times = [row.primary, row.secondary]
    .filter((window) => window.runsOutBeforeReset && window.projectedExhaustionAt)
    .map((window) => window.projectedExhaustionAt as string)
    .sort();
  return times[0] ?? null;
}

/**
 * One row per group plus the ungrouped keys, in the server's attention order
 * (R12); expanded rows survive polls because they are keyed by group id.
 */
export function GroupHealthTable({
  rows,
  trendsKnown,
  onAction,
}: {
  rows: GroupRow[];
  trendsKnown: boolean;
  onAction: (action: OverviewAction) => void;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const toggle = (key: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const onlyUngrouped = rows.length === 1 && rows[0].id === null;

  return (
    <section aria-labelledby="overview-groups-title" className="rounded-xl border bg-card">
      <div className="flex items-center justify-between border-b px-4 py-2">
        <h2 id="overview-groups-title" className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
          {t("overview.groups.title")}
        </h2>
        {onlyUngrouped ? <p className="text-xs text-muted-foreground">{t("overview.groups.noGroupsHint")}</p> : null}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-[11px] uppercase tracking-wider text-muted-foreground">
              <th className="w-8 px-2 py-2" />
              <th className="px-2 py-2 font-medium">{t("overview.columns.group")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.columns.accounts")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.windows.5h")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.windows.weekly")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.columns.exhaustion")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.columns.limitFill")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.columns.keys")}</th>
              <th className="px-2 py-2 text-right font-medium">{t("overview.columns.spendToday")}</th>
              <th className="px-2 py-2 font-medium">{t("overview.columns.spend7d")}</th>
              <th className="px-2 py-2" />
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const key = rowKey(row);
              const open = expanded.has(key);
              const exhaustion = exhaustionOf(row);
              const name = row.id === null ? t("overview.groups.ungrouped") : row.name;
              return (
                <Fragment key={key}>
                  <tr data-testid="overview-group-row" className={cn("border-b align-top", open ? "bg-muted/20" : undefined)}>
                    <td className="px-2 py-2">
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-6 w-6"
                        aria-expanded={open}
                        aria-label={t(open ? "overview.groups.collapse" : "overview.groups.expand", { name })}
                        onClick={() => toggle(key)}
                      >
                        {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                      </Button>
                    </td>
                    <td className="px-2 py-2">
                      <div className="flex items-center gap-2">
                        <span className="font-medium">{name}</span>
                        {row.attention !== "none" ? (
                          <Badge variant={row.attention === "critical" ? "destructive" : "secondary"} className="text-[10px]">
                            {t(`overview.severity.${row.attention}`)}
                          </Badge>
                        ) : null}
                      </div>
                      {row.id === null ? <span className="text-[11px] text-muted-foreground">{t("overview.groups.wholePool")}</span> : null}
                    </td>
                    <td className="px-2 py-2">
                      <HealthBar counts={row.accounts} />
                    </td>
                    <td className="px-2 py-2">
                      <WindowColumn health={row.primary} />
                    </td>
                    <td className="px-2 py-2">
                      <WindowColumn health={row.secondary} />
                    </td>
                    <td className="px-2 py-2 text-xs">
                      {exhaustion ? <span className="font-medium text-red-500">{clockLabel(exhaustion)}</span> : <span className="text-muted-foreground">—</span>}
                    </td>
                    <td className="px-2 py-2 tabular-nums">
                      <span className="font-medium">{percentLabel(row.limitFill.maxPercent)}</span>
                      {row.limitFill.keysAtOrAbove90 > 0 ? (
                        <span className="block text-[11px] text-amber-600">
                          {t("overview.groups.keysNearLimit", { count: row.limitFill.keysAtOrAbove90 })}
                        </span>
                      ) : null}
                    </td>
                    <td className="px-2 py-2 tabular-nums">
                      {row.keys.active}/{row.keys.total}
                    </td>
                    <td className="px-2 py-2 text-right font-medium tabular-nums">{usdLabel(row.spendTodayUsd)}</td>
                    <td className="px-2 py-2">
                      {trendsKnown && row.trend7d ? (
                        <div className="flex w-32 items-center gap-2">
                          <div className="h-5 w-16">
                            <SparklineChart data={trendPoints(row.trend7d)} color="#6366f1" index={0} height={20} />
                          </div>
                          <span className="tabular-nums text-xs">{usdLabel(row.spend7dUsd)}</span>
                        </div>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-2 py-2">
                      <div className="flex justify-end gap-1">
                        {row.id !== null ? (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="h-6 w-6"
                            aria-label={t("overview.groups.openGroup", { name })}
                            title={t("overview.groups.openGroup", { name })}
                            onClick={() => onAction({ kind: "open_group", target: row.id as string })}
                          >
                            <Users className="h-3.5 w-3.5" />
                          </Button>
                        ) : null}
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-6 w-6"
                          aria-label={t("overview.groups.openKeys", { name })}
                          title={t("overview.groups.openKeys", { name })}
                          onClick={() => onAction({ kind: "open_list", target: `/keys?group=${row.id === null ? "ungrouped" : encodeURIComponent(row.id)}` })}
                        >
                          <KeyRound className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                  {open ? (
                    <tr className="border-b">
                      <td colSpan={11} className="p-0">
                        <GroupRowDetail row={row} onAction={onAction} />
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
