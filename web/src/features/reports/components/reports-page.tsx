import { Download, Link2 } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { ApiError } from "@/lib/api-client";
import { copyToClipboard } from "@/utils/clipboard";
import { getErrorMessageOrNull } from "@/utils/errors";

import { keyUsageReportExportUrl, type KeyUsageReportParams } from "../api";
import { getBrowserReportsTimeZone, localDateISO } from "../date";
import { useKeyUsageReport } from "../hooks/use-key-usage-report";
import type { KeyUsageReport } from "../schemas";
import {
  isScopeEmpty,
  parseReportUrl,
  reportRangeError,
  resolveReportRange,
  writeReportUrl,
  type QuickPeriod,
  type ReportRange,
  type ReportScope,
  type ReportUrlState,
} from "../report-url";
import { GroupReportTable } from "./group-report-table";
import { KeyDailyChart } from "./key-daily-chart";
import { KeyReportSummary, KeyReportSummarySkeleton } from "./key-report-summary";
import { PerKeyTable, PerKeyTableSkeleton } from "./per-key-table";
import { PeriodPicker } from "./period-picker";
import { ScopePicker } from "./scope-picker";
import { trafficScope } from "../traffic-scope";
import { TrafficDetails } from "./traffic-details";

/**
 * Reports for API-key users (dashboard redesign use case 5): a selection of
 * groups and keys and one period, both kept in the URL, read from
 * GET /api/reports/keys without polling; traffic details load on demand.
 */
export function ReportsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [timezone] = useState(() => getBrowserReportsTimeZone());
  const state = useMemo(() => parseReportUrl(searchParams), [searchParams]);
  const today = localDateISO();
  // Quick periods follow the browser-local today on every render.
  const range = resolveReportRange(state.period);
  const rangeError = reportRangeError(range, today);
  const scopeEmpty = isScopeEmpty(state.scope);
  const directory = useDirectory();

  const params: KeyUsageReportParams = useMemo(
    () => ({
      groupIds: state.scope.groupIds,
      keyIds: state.scope.keyIds,
      ungrouped: state.scope.ungrouped,
      startDate: range.start,
      endDate: range.end,
      timezone,
    }),
    [range.end, range.start, state.scope, timezone],
  );
  const reportQuery = useKeyUsageReport(params, rangeError === null);
  // A failed refetch has no data of its own; the last report stays visible.
  const [lastReport, setLastReport] = useState<KeyUsageReport | undefined>(undefined);
  if (reportQuery.data && reportQuery.data !== lastReport) {
    setLastReport(reportQuery.data);
  }
  const report = reportQuery.data ?? lastReport;
  const reportCurrent = reportQuery.data !== undefined && !reportQuery.isPlaceholderData && rangeError === null;

  const update = useCallback(
    (next: Partial<ReportUrlState>) => {
      setSearchParams((current) => writeReportUrl({ ...parseReportUrl(current), ...next }, current), { replace: true });
    },
    [setSearchParams],
  );
  const onScopeChange = useCallback((scope: ReportScope) => update({ scope }), [update]);
  const onQuickPeriod = useCallback((period: QuickPeriod) => update({ period: { kind: "quick", period } }), [update]);
  const onCustomRange = useCallback(
    (next: ReportRange) => update({ period: { kind: "custom", from: next.start, to: next.end } }),
    [update],
  );

  const copyLink = async () => {
    try {
      if (await copyToClipboard(window.location.href)) {
        toast.success(t("reports.actions.linkCopied"));
        return;
      }
    } catch {
      // Reported below like a refused copy.
    }
    toast.error(t("components.copyButton.toasts.failed"));
  };

  const reportError = reportQuery.error;
  const reportErrorMessage =
    reportError instanceof ApiError && reportError.code === "report_too_large"
      ? t("reports.errors.tooLarge", { error: reportError.message })
      : t("reports.errors.data", { error: getErrorMessageOrNull(reportError) ?? "" });
  const directoryError = getErrorMessageOrNull(directory.error);
  const resolvedKeyIds = reportCurrent && report ? report.keys.map((key) => key.id) : null;

  return (
    <div className="animate-fade-in-up space-y-5">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">{t("reports.page.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("reports.page.subtitle")}</p>
      </div>

      <div className="space-y-3 rounded-xl border bg-card p-3" data-testid="reports-selection-bar">
        <div className="flex flex-wrap items-start gap-3">
          <ScopePicker
            directory={directory.data}
            loading={directory.isPending}
            scope={state.scope}
            onChange={onScopeChange}
          />
          <div className="ml-auto flex flex-wrap items-center gap-2">
            <span className="text-xs text-muted-foreground">{t("reports.timezone", { zone: timezone ?? "UTC" })}</span>
            <Button type="button" variant="outline" size="sm" className="h-8 text-xs" onClick={() => void copyLink()}>
              <Link2 className="h-3.5 w-3.5" aria-hidden="true" />
              {t("reports.actions.copyLink")}
            </Button>
            {rangeError === null ? (
              <Button asChild variant="outline" size="sm" className="h-8 text-xs">
                <a href={keyUsageReportExportUrl(params)} download>
                  <Download className="h-3.5 w-3.5" aria-hidden="true" />
                  {t("reports.actions.exportCsv")}
                </a>
              </Button>
            ) : (
              <Button type="button" variant="outline" size="sm" className="h-8 text-xs" disabled>
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
                {t("reports.actions.exportCsv")}
              </Button>
            )}
          </div>
        </div>
        <PeriodPicker
          period={state.period}
          range={range}
          today={today}
          rangeError={rangeError}
          onQuickPeriod={onQuickPeriod}
          onCustomRange={onCustomRange}
        />
      </div>

      {directoryError ? (
        <div className="flex flex-wrap items-center gap-3" role="alert" data-testid="reports-directory-error">
          <AlertMessage variant="error">{t("reports.errors.directory", { error: directoryError })}</AlertMessage>
          <Button type="button" variant="outline" size="sm" onClick={() => void directory.refetch()} disabled={directory.isFetching}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}

      {reportError && rangeError === null ? (
        <div className="flex flex-wrap items-center gap-3" role="alert" data-testid="reports-error">
          <AlertMessage variant="error">{reportErrorMessage}</AlertMessage>
          <Button type="button" variant="outline" size="sm" onClick={() => void reportQuery.refetch()} disabled={reportQuery.isFetching}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}

      {report ? (
        <div
          className={reportCurrent ? "space-y-5" : "space-y-5 opacity-60 transition-opacity"}
          aria-busy={reportQuery.isFetching || undefined}
          data-testid="reports-content"
        >
          <KeyReportSummary report={report} />
          <GroupReportTable groups={report.groups} />
          <PerKeyTable report={report} />
          <KeyDailyChart
            start={report.period.start}
            end={report.period.end}
            series={report.series}
            daily={report.daily}
            empty={report.summary.requests === 0}
          />
        </div>
      ) : reportError || rangeError !== null ? null : (
        <div className="space-y-5" data-testid="reports-skeleton">
          <KeyReportSummarySkeleton />
          <PerKeyTableSkeleton />
        </div>
      )}

      <TrafficDetails
        range={range}
        timezone={timezone}
        enabled={rangeError === null}
        scope={trafficScope(scopeEmpty, resolvedKeyIds)}
      />
    </div>
  );
}
