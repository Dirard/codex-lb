import { useId } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

import { isISODate } from "../date";
import {
  QUICK_PERIODS,
  type QuickPeriod,
  type ReportPeriodSelection,
  type ReportRange,
  type ReportRangeError,
} from "../report-url";

const QUICK_PERIOD_LABEL_KEYS: Record<QuickPeriod, string> = {
  today: "reports.period.today",
  yesterday: "reports.period.yesterday",
  "7d": "reports.period.7d",
  "30d": "reports.period.30d",
  "90d": "reports.period.90d",
  this_month: "reports.period.thisMonth",
  last_month: "reports.period.lastMonth",
};

const RANGE_ERROR_KEYS: Record<ReportRangeError, string> = {
  inverted: "reports.filters.invalidDateRange",
  future: "reports.period.futureDate",
  too_long: "reports.period.tooLong",
};

export type PeriodPickerProps = {
  period: ReportPeriodSelection;
  range: ReportRange;
  today: string;
  rangeError: ReportRangeError | null;
  onQuickPeriod: (period: QuickPeriod) => void;
  onCustomRange: (range: ReportRange) => void;
};

/**
 * Quick periods and custom dates of the Reports page. The selected quick
 * period is highlighted; editing a date switches to a custom period. The
 * native bounds keep the start on or before the end and both on or before
 * the browser-local today; a bypassed invalid range marks both inputs.
 */
export function PeriodPicker({ period, range, today, rangeError, onQuickPeriod, onCustomRange }: PeriodPickerProps) {
  const { t } = useTranslation();
  const errorId = useId();
  const invalid = rangeError !== null;
  const startMax = range.end && range.end < today ? range.end : today;

  return (
    <div className="flex flex-wrap items-center gap-2">
      <div role="group" aria-label={t("reports.period.label")} className="flex flex-wrap items-center gap-1">
        {QUICK_PERIODS.map((quick) => {
          const selected = period.kind === "quick" && period.period === quick;
          return (
            <Button
              key={quick}
              type="button"
              size="sm"
              variant={selected ? "default" : "outline"}
              aria-pressed={selected}
              className="h-8 px-2.5 text-xs"
              onClick={() => onQuickPeriod(quick)}
            >
              {t(QUICK_PERIOD_LABEL_KEYS[quick])}
            </Button>
          );
        })}
      </div>
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <input
            type="date"
            name="report-start-date"
            autoComplete="off"
            aria-label={t("reports.filters.startDate")}
            aria-invalid={invalid || undefined}
            aria-describedby={invalid ? errorId : undefined}
            max={startMax}
            value={range.start}
            onChange={(event) => {
              if (isISODate(event.target.value)) {
                onCustomRange({ start: event.target.value, end: range.end });
              }
            }}
            className="h-8 rounded-md border bg-transparent px-2 text-xs text-foreground aria-invalid:border-destructive aria-invalid:ring-destructive/20"
          />
          <span aria-hidden="true" className="text-xs text-muted-foreground">
            —
          </span>
          <input
            type="date"
            name="report-end-date"
            autoComplete="off"
            aria-label={t("reports.filters.endDate")}
            aria-invalid={invalid || undefined}
            aria-describedby={invalid ? errorId : undefined}
            min={range.start || undefined}
            max={today}
            value={range.end}
            onChange={(event) => {
              if (isISODate(event.target.value)) {
                onCustomRange({ start: range.start, end: event.target.value });
              }
            }}
            className="h-8 rounded-md border bg-transparent px-2 text-xs text-foreground aria-invalid:border-destructive aria-invalid:ring-destructive/20"
          />
        </div>
        {rangeError ? (
          <p id={errorId} aria-live="polite" className="text-xs text-destructive">
            {t(RANGE_ERROR_KEYS[rangeError])}
          </p>
        ) : null}
      </div>
    </div>
  );
}
