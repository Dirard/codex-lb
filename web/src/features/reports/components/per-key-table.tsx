import { ArrowDown, ArrowUp, ArrowUpDown, ChevronDown, ChevronRight, KeyRound } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ownerKeyPageLink } from "@/features/key-page/owner-link";
import { agoLabel } from "@/features/overview/overview-format";
import { cn } from "@/lib/utils";
import { copyToClipboard } from "@/utils/clipboard";
import { formatLocalDateTimeSeconds } from "@/utils/formatters";

import {
  buildKeySections,
  DEFAULT_PER_KEY_SORT,
  type PerKeySort,
  type PerKeySortColumn,
  type Sortable,
} from "../key-sections";
import { costLabel, countLabel, errorRate, limitLabel, rateLabel, shareLabel, tokenLabel } from "../report-format";
import type { KeyUsageReport } from "../schemas";

/** Rows shown per group before "Show all". */
export const GROUP_ROW_LIMIT = 20;

type ColumnDef = { column: PerKeySortColumn; labelKey: string; align: "left" | "right" };

const NAME_COLUMN: ColumnDef = { column: "name", labelKey: "reports.columns.key", align: "left" };

const SORTABLE_COLUMNS: ColumnDef[] = [
  { column: "requests", labelKey: "reports.columns.requests", align: "right" },
  { column: "inputTokens", labelKey: "reports.columns.inputTokens", align: "right" },
  { column: "outputTokens", labelKey: "reports.columns.outputTokens", align: "right" },
  { column: "cachedInputTokens", labelKey: "reports.columns.cachedTokens", align: "right" },
  { column: "cost", labelKey: "reports.columns.cost", align: "right" },
  { column: "share", labelKey: "reports.columns.share", align: "right" },
  { column: "errors", labelKey: "reports.columns.errors", align: "right" },
  { column: "errorRate", labelKey: "reports.columns.errorRate", align: "right" },
  { column: "lastUsed", labelKey: "reports.columns.lastUsed", align: "left" },
  { column: "maxLimit", labelKey: "reports.columns.fullestLimit", align: "right" },
];

function SortHeader({
  def,
  sort,
  onSort,
}: {
  def: ColumnDef;
  sort: PerKeySort;
  onSort: (column: PerKeySortColumn) => void;
}) {
  const { t } = useTranslation();
  const active = sort.column === def.column;
  const Icon = !active ? ArrowUpDown : sort.direction === "asc" ? ArrowUp : ArrowDown;
  return (
    <th
      scope="col"
      aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}
      className={cn("px-3 py-2 font-medium", def.align === "right" ? "text-right" : "text-left")}
    >
      <button
        type="button"
        onClick={() => onSort(def.column)}
        className={cn(
          "inline-flex items-center gap-1 uppercase hover:text-foreground",
          active ? "text-foreground" : undefined,
          def.align === "right" ? "flex-row-reverse" : undefined,
        )}
      >
        {t(def.labelKey)}
        <Icon className={cn("h-3 w-3", active ? "opacity-100" : "opacity-40")} aria-hidden="true" />
      </button>
    </th>
  );
}

function MeasureCells({ item, strong = false }: { item: Sortable; strong?: boolean }) {
  const m = item.measures;
  const cell = cn("px-3 py-2 text-right tabular-nums", strong ? "font-medium" : undefined);
  return (
    <>
      <td className={cell}>{countLabel(m.requests)}</td>
      <td className={cell}>{tokenLabel(m.inputTokens)}</td>
      <td className={cell}>{tokenLabel(m.outputTokens)}</td>
      <td className={cell}>{tokenLabel(m.cachedInputTokens)}</td>
      <td className={cell}>{costLabel(m.costUsd)}</td>
      <td className={cell}>{shareLabel(item.share)}</td>
      <td className={cell}>{countLabel(m.errors)}</td>
      <td className={cell}>{rateLabel(errorRate(m))}</td>
    </>
  );
}

function limitTone(percent: number | null): string | undefined {
  if (percent === null) {
    return "text-muted-foreground";
  }
  if (percent >= 100) {
    return "text-red-600 dark:text-red-400";
  }
  if (percent >= 90) {
    return "text-amber-600 dark:text-amber-400";
  }
  return undefined;
}

// Name, the sortable measures and the actions; the group is the section header.
const COLUMN_COUNT = 1 + SORTABLE_COLUMNS.length + 1;

/**
 * The keys of the report grouped by group with a subtotal row per group
 * (R32): sortable by every measure, filterable by name, prefix or group,
 * idle keys hidden by default, groups collapsible and limited to 20 rows
 * until "Show all", with Open and Copy owner link per key.
 */
export function PerKeyTable({ report }: { report: KeyUsageReport }) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState("");
  const [showIdle, setShowIdle] = useState(false);
  const [sort, setSort] = useState<PerKeySort>(DEFAULT_PER_KEY_SORT);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const [now] = useState(() => Date.now());
  const ungroupedLabel = t("reports.ungroupedKeys");
  const { sections, idleCount, matchCount } = useMemo(
    () => buildKeySections(report, { filter, showIdle, sort, ungroupedLabel }),
    [filter, report, showIdle, sort, ungroupedLabel],
  );
  const ownerLink = ownerKeyPageLink({ from: report.period.start, to: report.period.end });

  const onSort = (column: PerKeySortColumn) =>
    setSort((current) =>
      current.column === column
        ? { column, direction: current.direction === "asc" ? "desc" : "asc" }
        : { column, direction: column === "name" ? "asc" : "desc" },
    );
  const toggleIn = (set: ReadonlySet<string>, id: string) => {
    const next = new Set(set);
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
    }
    return next;
  };
  const copyOwnerLink = async () => {
    try {
      if (await copyToClipboard(ownerLink)) {
        toast.success(t("reports.keys.ownerLinkCopied"));
        return;
      }
    } catch {
      // Reported below like a refused copy.
    }
    toast.error(t("components.copyButton.toasts.failed"));
  };

  let empty: { title: string; description?: string } | null = null;
  if (report.selection.resolvedKeyCount === 0 || report.keys.length === 0) {
    empty =
      report.selection.groupIds.length > 0 && report.selection.keyIds.length === 0 && !report.selection.ungrouped
        ? { title: t("reports.keys.groupWithoutKeys") }
        : { title: t("reports.keys.noKeys") };
  } else if (sections.length === 0) {
    empty =
      matchCount === 0
        ? { title: t("reports.keys.noFilterMatches") }
        : { title: t("reports.empty.noUsage"), description: t("reports.keys.idleHidden", { count: idleCount }) };
  }

  return (
    <section aria-labelledby="reports-keys-title" className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2">
        <h2 id="reports-keys-title" className="text-[13px] font-medium tracking-wider text-muted-foreground uppercase">
          {t("reports.keys.title")}
        </h2>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Input
            type="search"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            aria-label={t("reports.keys.filter")}
            placeholder={t("reports.keys.filterPlaceholder")}
            className="h-8 w-52 text-xs"
          />
          {idleCount > 0 || showIdle ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-8 text-xs"
              aria-pressed={showIdle}
              onClick={() => setShowIdle((value) => !value)}
            >
              {showIdle ? t("reports.keys.hideIdle") : t("reports.keys.showIdle", { count: idleCount })}
            </Button>
          ) : null}
        </div>
      </div>
      {empty ? (
        <div className="p-4">
          <EmptyState icon={KeyRound} title={empty.title} description={empty.description} />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-[11px] tracking-wider text-muted-foreground uppercase">
                <SortHeader def={NAME_COLUMN} sort={sort} onSort={onSort} />
                {SORTABLE_COLUMNS.map((def) => (
                  <SortHeader key={def.column} def={def} sort={sort} onSort={onSort} />
                ))}
                <th scope="col" className="px-3 py-2">
                  <span className="sr-only">{t("reports.columns.actions")}</span>
                </th>
              </tr>
            </thead>
            {sections.map((section) => {
              const isCollapsed = collapsed.has(section.id);
              const showAll = expanded.has(section.id);
              const rows = showAll ? section.rows : section.rows.slice(0, GROUP_ROW_LIMIT);
              return (
                <tbody key={section.id} data-testid="per-key-section">
                  <tr data-testid="per-key-subtotal" className="border-b bg-muted/30">
                    <td className="px-3 py-2">
                      <button
                        type="button"
                        aria-expanded={!isCollapsed}
                        onClick={() => setCollapsed((current) => toggleIn(current, section.id))}
                        className="inline-flex items-center gap-1.5 font-medium hover:underline"
                      >
                        {isCollapsed ? (
                          <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
                        ) : (
                          <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
                        )}
                        {section.name}
                      </button>
                      <span className="ml-2 text-xs text-muted-foreground">
                        {t("reports.keys.subtotal", { active: section.activeKeys, total: section.keyCount })}
                      </span>
                    </td>
                    <MeasureCells item={section.subtotal} strong />
                    <td className="px-3 py-2 text-xs text-muted-foreground">
                      {agoLabel(section.subtotal.lastUsedAt, now) ?? "—"}
                    </td>
                    <td className={cn("px-3 py-2 text-right tabular-nums", limitTone(section.subtotal.maxLimitPercent))}>
                      {limitLabel(section.subtotal.maxLimitPercent)}
                    </td>
                    <td />
                  </tr>
                  {isCollapsed
                    ? null
                    : rows.map((key) => (
                        <tr key={key.id} data-testid="per-key-row" className="border-b last:border-0">
                          <td className="px-3 py-2">
                            <Link className="font-medium hover:underline" to={`/keys/${encodeURIComponent(key.id)}`}>
                              {key.name}
                            </Link>
                            {key.status !== "active" ? (
                              <Badge variant="secondary" className="ml-1.5 text-[10px]">
                                {t(`common.states.${key.status}`)}
                              </Badge>
                            ) : null}
                            <span className="block font-mono text-[11px] text-muted-foreground">{key.prefix}</span>
                          </td>
                          <MeasureCells item={key} />
                          <td
                            className="px-3 py-2 text-xs text-muted-foreground"
                            title={key.lastUsedAt ? formatLocalDateTimeSeconds(key.lastUsedAt) : undefined}
                          >
                            {agoLabel(key.lastUsedAt, now) ?? "—"}
                          </td>
                          <td className={cn("px-3 py-2 text-right tabular-nums", limitTone(key.maxLimitPercent))}>
                            {limitLabel(key.maxLimitPercent)}
                          </td>
                          <td className="px-3 py-2">
                            <div className="flex items-center justify-end gap-1">
                              <Button asChild size="xs" variant="ghost">
                                <Link to={`/keys/${encodeURIComponent(key.id)}`}>{t("reports.keys.open")}</Link>
                              </Button>
                              <Button
                                type="button"
                                size="xs"
                                variant="ghost"
                                title={ownerLink}
                                onMouseDown={(event) => event.preventDefault()}
                                onClick={() => void copyOwnerLink()}
                              >
                                {t("reports.keys.copyOwnerLink")}
                              </Button>
                            </div>
                          </td>
                        </tr>
                      ))}
                  {!isCollapsed && section.rows.length > GROUP_ROW_LIMIT ? (
                    <tr className="border-b">
                      <td colSpan={COLUMN_COUNT} className="px-3 py-1.5">
                        <Button
                          type="button"
                          size="xs"
                          variant="ghost"
                          onClick={() => setExpanded((current) => toggleIn(current, section.id))}
                        >
                          {showAll ? t("reports.keys.showFewer") : t("reports.keys.showAll", { count: section.rows.length })}
                        </Button>
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              );
            })}
          </table>
        </div>
      )}
    </section>
  );
}

export function PerKeyTableSkeleton() {
  return (
    <div className="space-y-2 rounded-xl border bg-card p-4" data-testid="per-key-table-skeleton">
      {Array.from({ length: 8 }, (_, index) => (
        <Skeleton key={index} className="h-8 w-full" />
      ))}
    </div>
  );
}
