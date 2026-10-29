import { ChevronDown, ChevronRight } from "lucide-react";
import { lazy, Suspense, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { getErrorMessageOrNull } from "@/utils/errors";

import { useReports, type TrafficReportParams } from "../hooks/use-reports";
import type { ReportRange } from "../report-url";
import { TRAFFIC_DETAILS_KEY_LIMIT, type TrafficScope } from "../traffic-scope";
import type { ModelDistributionDonutProps } from "./model-distribution-donut";
import type { TimeToFirstTokenChartProps } from "./time-to-first-token-chart";
import type { TokensPerSecondChartProps } from "./tokens-per-second-chart";
import type { UseragentDistributionDonutProps } from "./useragent-distribution-donut";

const ModelDistributionDonut = lazy(() =>
  import("./model-distribution-donut").then((module) => ({
    default: (props: ModelDistributionDonutProps) => <module.ModelDistributionDonut {...props} />,
  })),
);
const UseragentDistributionDonut = lazy(() =>
  import("./useragent-distribution-donut").then((module) => ({
    default: (props: UseragentDistributionDonutProps) => <module.UseragentDistributionDonut {...props} />,
  })),
);
const TimeToFirstTokenChart = lazy(() =>
  import("./time-to-first-token-chart").then((module) => ({
    default: (props: TimeToFirstTokenChartProps) => <module.TimeToFirstTokenChart {...props} />,
  })),
);
const TokensPerSecondChart = lazy(() =>
  import("./tokens-per-second-chart").then((module) => ({
    default: (props: TokensPerSecondChartProps) => <module.TokensPerSecondChart {...props} />,
  })),
);

export type TrafficDetailsProps = {
  range: ReportRange;
  timezone: string | undefined;
  /** False while the period is invalid: nothing is requested. */
  enabled: boolean;
  scope: TrafficScope;
};

function TrafficDetailsBody({ range, timezone, scope }: Omit<TrafficDetailsProps, "enabled">) {
  const { t } = useTranslation();
  const params: TrafficReportParams = {
    startDate: range.start,
    endDate: range.end,
    timezone,
    apiKeyIds: scope.kind === "keys" ? scope.keyIds : [],
  };
  const fetchable = scope.kind === "all" || scope.kind === "keys";
  const query = useReports(params, fetchable);
  const error = getErrorMessageOrNull(query.error);

  if (scope.kind === "too_many") {
    return <AlertMessage variant="warning">{t("reports.traffic.tooManyKeys", { count: scope.count, limit: TRAFFIC_DETAILS_KEY_LIMIT })}</AlertMessage>;
  }
  if (scope.kind === "none") {
    return <p className="text-sm text-muted-foreground">{t("reports.keys.noKeys")}</p>;
  }
  if (error && !query.data) {
    return (
      <div className="flex flex-wrap items-center gap-3" role="alert">
        <AlertMessage variant="error">{t("reports.traffic.failed", { error })}</AlertMessage>
        <Button type="button" variant="outline" size="sm" onClick={() => void query.refetch()} disabled={query.isFetching}>
          {t("common.actions.retry")}
        </Button>
      </div>
    );
  }
  if (!query.data) {
    return (
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2" data-testid="traffic-details-loading">
        <Skeleton className="h-[220px] rounded-xl" />
        <Skeleton className="h-[220px] rounded-xl" />
      </div>
    );
  }
  const data = query.data;
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <div className="space-y-4 lg:col-span-1">
        <Suspense fallback={<div className="h-[220px] rounded-xl border bg-card" />}>
          <ModelDistributionDonut data={data.byModel} />
        </Suspense>
        <Suspense fallback={<div className="h-[220px] rounded-xl border bg-card" />}>
          <UseragentDistributionDonut data={data.byUseragent} />
        </Suspense>
      </div>
      <div className="space-y-4 lg:col-span-2">
        <Suspense fallback={<div className="h-[270px] rounded-xl border bg-card" />}>
          <TimeToFirstTokenChart startDate={range.start} endDate={range.end} data={data.daily} />
        </Suspense>
        <Suspense fallback={<div className="h-[270px] rounded-xl border bg-card" />}>
          <TokensPerSecondChart startDate={range.start} endDate={range.end} data={data.daily} />
        </Suspense>
      </div>
    </div>
  );
}

/**
 * Collapsed model, user-agent and latency details of the selection from
 * GET /api/reports, requested only once expanded (R32).
 */
export function TrafficDetails({ range, timezone, enabled, scope }: TrafficDetailsProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const contentId = useId();
  return (
    <section className="rounded-xl border bg-card" aria-labelledby={`${contentId}-title`}>
      <h2 id={`${contentId}-title`} className="m-0">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={contentId}
          onClick={() => setOpen((value) => !value)}
          className="flex w-full items-center gap-2 px-4 py-3 text-left text-sm font-semibold text-foreground hover:bg-muted/40"
        >
          {open ? <ChevronDown className="h-4 w-4" aria-hidden="true" /> : <ChevronRight className="h-4 w-4" aria-hidden="true" />}
          {t("reports.traffic.title")}
          <span className="ml-2 text-xs font-normal text-muted-foreground">{t("reports.traffic.description")}</span>
        </button>
      </h2>
      {open ? (
        <div id={contentId} className="border-t p-4">
          {enabled ? (
            <TrafficDetailsBody range={range} timezone={timezone} scope={scope} />
          ) : (
            <p className="text-sm text-muted-foreground">{t("reports.traffic.invalidPeriod")}</p>
          )}
        </div>
      ) : null}
    </section>
  );
}
