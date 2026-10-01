import { lazy, Suspense, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/lib/api-client";
import { ReportsSummaryCards } from "@/features/reports/components/reports-summary-cards";
import { DailyDetailTable } from "@/features/reports/components/daily-detail-table";
import { daysAgoLocalISO, getBrowserReportsTimeZone, isReportDateRangeValid, localDateISO } from "@/features/reports/date";
import { deleteKeyReportSession, getKeyReport } from "./api";
import { KeyReportLimits } from "./key-report-limits";

const CostPerDayChart = lazy(() => import("@/features/reports/components/cost-per-day-chart").then((m) => ({ default: m.CostPerDayChart })));
const TokensPerDayChart = lazy(() => import("@/features/reports/components/tokens-per-day-chart").then((m) => ({ default: m.TokensPerDayChart })));
const ModelDistributionDonut = lazy(() => import("@/features/reports/components/model-distribution-donut").then((m) => ({ default: m.ModelDistributionDonut })));

export function KeyReportsPage({ onExit }: { onExit: (message?: string) => void }) {
  const { t } = useTranslation();
  return (
    <main className="mx-auto min-h-screen w-full max-w-[1500px] space-y-6 px-4 py-8 sm:px-6">
      <header>
        <h1 className="text-2xl font-semibold">{t("keyReports.title")}</h1>
        <p className="text-muted-foreground text-sm">{t("keyReports.description")}</p>
      </header>
      <ReportSession onExit={onExit} />
    </main>
  );
}

// Each login owns a separate cache; neither credentials nor reports enter admin queries.
function ReportSession({ onExit }: { onExit: (message?: string) => void }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 30_000 } } }));
  useEffect(() => () => {
    void client.cancelQueries();
    client.clear();
  }, [client]);
  return <QueryClientProvider client={client}><ReportView onExit={onExit} /></QueryClientProvider>;
}

function ReportView({ onExit }: { onExit: (message?: string) => void }) {
  const { t } = useTranslation();
  const [filters, setFilters] = useState(() => ({ startDate: daysAgoLocalISO(6), endDate: localDateISO(), timezone: getBrowserReportsTimeZone(), model: "" }));
  const [draft, setDraft] = useState(filters);
  const [signingOut, setSigningOut] = useState(false);
  const [logoutFailed, setLogoutFailed] = useState(false);
  const query = useQuery({
    queryKey: ["key-reports", filters],
    queryFn: ({ signal }) => getKeyReport(filters, signal),
  });
  const unauthorized = query.error instanceof ApiError && query.error.status === 401;
  useEffect(() => {
    if (unauthorized) onExit(t("keyReports.invalidKey"));
  }, [unauthorized, onExit, t]);
  const validDates = isReportDateRangeValid(draft.startDate, draft.endDate);
  const report = query.error ? undefined : query.data;

  const signOut = async () => {
    setSigningOut(true);
    setLogoutFailed(false);
    try {
      await deleteKeyReportSession();
      onExit();
    } catch {
      setLogoutFailed(true);
    } finally {
      setSigningOut(false);
    }
  };

  const apply = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!validDates || query.isFetching) return;
    const next = { ...draft, model: draft.model.trim() };
    if (next.startDate === filters.startDate && next.endDate === filters.endDate && next.model === filters.model) {
      void query.refetch();
    } else {
      setFilters(next);
    }
  };

  return (
    <section className="space-y-6">
      <div className="flex items-center justify-between gap-4">
        <p className="text-muted-foreground text-sm">{t("keyReports.readOnly")}</p>
        <Button variant="outline" disabled={signingOut} onClick={() => void signOut()}>{t("keyReports.signOut")}</Button>
      </div>
      {logoutFailed ? <div role="alert"><AlertMessage variant="error">{t("keyReports.signOutFailed")}</AlertMessage></div> : null}
      <form onSubmit={apply} className="flex flex-wrap items-end gap-4 rounded-xl border bg-card p-5">
        <div className="space-y-2">
          <Label htmlFor="key-report-start">{t("keyReports.startDate")}</Label>
          <Input id="key-report-start" type="date" value={draft.startDate} max={draft.endDate || undefined}
            onChange={(event) => setDraft({ ...draft, startDate: event.target.value })} />
        </div>
        <div className="space-y-2">
          <Label htmlFor="key-report-end">{t("keyReports.endDate")}</Label>
          <Input id="key-report-end" type="date" value={draft.endDate} min={draft.startDate || undefined}
            onChange={(event) => setDraft({ ...draft, endDate: event.target.value })} />
        </div>
        <div className="space-y-2">
          <Label htmlFor="key-report-model">{t("keyReports.model")}</Label>
          <Input id="key-report-model" value={draft.model} maxLength={256}
            onChange={(event) => setDraft({ ...draft, model: event.target.value })} />
        </div>
        <Button type="submit" disabled={!validDates || query.isFetching}>{t("keyReports.refresh")}</Button>
      </form>
      {!validDates ? <AlertMessage variant="error">{t("keyReports.invalidDates")}</AlertMessage> : null}
      {query.error && !unauthorized ? <div role="alert"><AlertMessage variant="error">{t("keyReports.loadFailed")}</AlertMessage></div> : null}
      {query.isPending ? <p role="status">{t("common.loading")}</p> : null}
      {report ? <>
        <KeyReportLimits limits={report.limits} group={report.group} />
        <ReportsSummaryCards summary={report.summary} comparison={report.comparison} />
        <Suspense fallback={<p role="status">{t("common.loading")}</p>}>
          <div className="grid gap-4 lg:grid-cols-2">
            <CostPerDayChart startDate={filters.startDate} endDate={filters.endDate} data={report.daily} />
            <TokensPerDayChart startDate={filters.startDate} endDate={filters.endDate} data={report.daily} />
          </div>
          <ModelDistributionDonut data={report.byModel} />
        </Suspense>
        <DailyDetailTable startDate={filters.startDate} endDate={filters.endDate} data={report.daily} />
      </> : null}
    </section>
  );
}
