import { useCallback, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { Columns3, RotateCcw } from "lucide-react";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SpinnerBlock } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import {
  DEFAULT_REQUESTS_TIMEFRAME,
  parseDashboardView,
  type DashboardView,
  type RequestsTimeframe,
} from "@/features/dashboard/schemas";
import { accountLabelSources } from "@/features/requests/account-labels";
import { ConversationsView } from "@/features/requests/components/conversations-view";
import { RecentRequestsTable } from "@/features/requests/components/recent-requests-table";
import { RequestFilters } from "@/features/requests/components/request-filters";
import { RequestsTimeframeSelect } from "@/features/requests/components/requests-timeframe-select";
import { DashboardViewSelector } from "@/features/requests/components/requests-view-selector";
import { useConversations } from "@/features/requests/hooks/use-conversations";
import { useRequestLogTablePreferences } from "@/features/requests/hooks/use-request-log-table-preferences";
import { useRequestLogs } from "@/features/requests/hooks/use-request-logs";
import { REQUEST_LOG_COLUMN_OPTIONS } from "@/features/requests/request-log-columns";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { REQUEST_STATUS_LABELS } from "@/utils/constants";
import { formatCurrency, formatModelLabel, formatSlug } from "@/utils/formatters";

const MODEL_OPTION_DELIMITER = ":::";

/**
 * The request log and the conversation list for diagnosis (dashboard
 * redesign use case 3): one period picker defaulting to 24 hours, no polling
 * unless Live is on, and filter options fetched for the selected timeframe
 * only when a filter opens. Key and account names come from the directory.
 */
export function RequestsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [live, setLive] = useState(false);
  const [optionsRequested, setOptionsRequested] = useState(false);
  const {
    visibleColumns,
    columnWidths,
    toggleColumn,
    setColumnWidth,
    restoreDefaultLayout,
  } = useRequestLogTablePreferences();
  const view = useMemo(() => parseDashboardView(searchParams.get("view")), [searchParams]);
  const directoryQuery = useDirectory();
  const accounts = useMemo(() => accountLabelSources(directoryQuery.data), [directoryQuery.data]);
  const conversationsState = useConversations({ enabled: view === "conversations", live });
  const { filters, emptyStateFiltersApplied, logsQuery, optionsQuery, updateFilters } = useRequestLogs({
    enabled: view === "request-logs",
    live,
    optionsEnabled: optionsRequested,
  });
  const blurred = usePrivacyStore((s) => s.blurred);

  const handleViewChange = useCallback(
    (next: DashboardView) => {
      const params = new URLSearchParams(searchParams);
      if (next === "request-logs") {
        params.delete("view");
      } else {
        params.set("view", next);
      }
      setSearchParams(params);
    },
    [searchParams, setSearchParams],
  );

  // One period for both views; changing it restarts both lists.
  const handleTimeframeChange = useCallback(
    (timeframe: RequestsTimeframe) => {
      const params = new URLSearchParams(searchParams);
      if (timeframe === DEFAULT_REQUESTS_TIMEFRAME) {
        params.delete("timeframe");
      } else {
        params.set("timeframe", timeframe);
      }
      for (const key of ["offset", "conversationOffset"]) {
        if (params.has(key)) {
          params.set(key, "0");
        }
      }
      setSearchParams(params);
    },
    [searchParams, setSearchParams],
  );

  const accountOptions = useMemo(
    () =>
      (directoryQuery.data?.accounts ?? []).map((account) => ({
        value: account.id,
        label: account.label,
        isEmail: account.label === account.email,
      })),
    [directoryQuery.data?.accounts],
  );
  const apiKeyOptions = useMemo(
    () =>
      (directoryQuery.data?.keys ?? []).map((key) => ({
        value: key.id,
        label: key.prefix ? `${key.name} · ${key.prefix}` : key.name,
      })),
    [directoryQuery.data?.keys],
  );
  const modelOptions = useMemo(
    () =>
      (optionsQuery.data?.modelOptions ?? []).map((option) => ({
        value: `${option.model}${MODEL_OPTION_DELIMITER}${option.reasoningEffort ?? ""}`,
        label: formatModelLabel(option.model, option.reasoningEffort),
      })),
    [optionsQuery.data?.modelOptions],
  );
  const statusOptions = useMemo(
    () =>
      (optionsQuery.data?.statuses ?? []).map((status) => ({
        value: status,
        label: t(`dashboard.requestStatus.${status}`, { defaultValue: REQUEST_STATUS_LABELS[status] ?? formatSlug(status) }),
      })),
    [optionsQuery.data?.statuses, t],
  );

  const logPage = logsQuery.data;
  const conversationSummary = useMemo(() => {
    const conv = logPage?.conversation;
    if (!conv || !filters.conversationId) {
      return null;
    }
    const cost = formatCurrency(conv.aggregatedCostUsd);
    const count = conv.requestCount;
    const suffixParts: string[] = [t(`requests.timeframes.${filters.timeframe}`)];
    if (filters.statuses.length > 0) {
      suffixParts.push(
        filters.statuses
          .map((s) => t(`dashboard.requestStatus.${s}`, { defaultValue: REQUEST_STATUS_LABELS[s] ?? s }))
          .join(", "),
      );
    }
    if (filters.accountIds.length > 0) {
      suffixParts.push(
        filters.accountIds
          .map((id) => {
            const option = accountOptions.find((o) => o.value === id);
            if (!option) return t("dashboard.filters.accounts");
            return blurred && option.isEmail ? id.slice(0, 8) : option.label;
          })
          .join(", "),
      );
    }
    if (filters.apiKeyIds.length > 0) {
      suffixParts.push(
        filters.apiKeyIds.map((id) => apiKeyOptions.find((o) => o.value === id)?.label ?? t("dashboard.filters.apiKeys")).join(", "),
      );
    }
    if (filters.search) {
      suffixParts.push(`"${filters.search}"`);
    }
    const codeClass = "rounded bg-muted px-1 py-0.5 text-xs font-mono";
    return (
      <Trans
        i18nKey="dashboard.conversation.summaryWithFilters"
        values={{ id: filters.conversationId, count, cost, filters: suffixParts.join(", ") }}
        components={[
          <code key="id" className={codeClass} />,
          <code key="count" className={codeClass} />,
          <code key="cost" className={codeClass} />,
        ]}
      />
    );
  }, [logPage?.conversation, filters, t, accountOptions, apiKeyOptions, blurred]);

  return (
    <div className="animate-fade-in-up space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("requests.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("requests.subtitle")}</p>
        </div>
        <div className="flex items-center gap-3">
          <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
            <Switch checked={live} onCheckedChange={setLive} aria-label={t("requests.live")} />
            {t("requests.live")}
          </label>
          <RequestsTimeframeSelect value={filters.timeframe} onChange={handleTimeframeChange} />
        </div>
      </div>

      <section className="space-y-4">
        <div className="flex flex-wrap items-center gap-3">
          <DashboardViewSelector value={view} onChange={handleViewChange} />
          <div className="h-px min-w-8 flex-1 bg-border" />
          {view === "request-logs" ? (
            <>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button type="button" variant="outline" size="sm">
                    <Columns3 className="mr-2 h-4 w-4" />
                    {t("dashboard.requests.columnLayout.columns", { count: visibleColumns.length })}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-52">
                  <DropdownMenuLabel>{t("dashboard.requests.columnLayout.visibleColumns")}</DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  {REQUEST_LOG_COLUMN_OPTIONS.map((column) => {
                    const isVisible = visibleColumns.includes(column.id);
                    return (
                      <DropdownMenuCheckboxItem
                        key={column.id}
                        checked={isVisible}
                        disabled={isVisible && visibleColumns.length === 1}
                        onCheckedChange={() => toggleColumn(column.id)}
                        onSelect={(event) => event.preventDefault()}
                      >
                        {t(column.translationKey)}
                      </DropdownMenuCheckboxItem>
                    );
                  })}
                </DropdownMenuContent>
              </DropdownMenu>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={t("dashboard.requests.columnLayout.restoreDefault")}
                title={t("dashboard.requests.columnLayout.restoreDefault")}
                onClick={restoreDefaultLayout}
              >
                <RotateCcw className="h-4 w-4" />
              </Button>
            </>
          ) : null}
        </div>

        {view === "conversations" ? (
          <ConversationsView state={conversationsState} accounts={accounts} />
        ) : (
          <>
            <RequestFilters
              filters={filters}
              accountOptions={accountOptions}
              apiKeyOptions={apiKeyOptions}
              modelOptions={modelOptions}
              statusOptions={statusOptions}
              onOptionsRequested={() => setOptionsRequested(true)}
              onSearchChange={(search) => updateFilters({ search, offset: 0 })}
              onAccountChange={(accountIds) => updateFilters({ accountIds, offset: 0 })}
              onApiKeyChange={(apiKeyIds) => updateFilters({ apiKeyIds, offset: 0 })}
              onModelChange={(selected) => updateFilters({ modelOptions: selected, offset: 0 })}
              onStatusChange={(statuses) => updateFilters({ statuses, offset: 0 })}
              onConversationDismiss={() => updateFilters({ conversationId: null, offset: 0 })}
              onReset={() =>
                updateFilters({
                  search: "",
                  accountIds: [],
                  apiKeyIds: [],
                  modelOptions: [],
                  statuses: [],
                  conversationId: null,
                  offset: 0,
                })
              }
            />
            {optionsQuery.error instanceof Error ? (
              <AlertMessage variant="error">{optionsQuery.error.message}</AlertMessage>
            ) : null}
            {conversationSummary ? (
              <div className="rounded-xl border bg-card p-4">
                <p className="text-sm text-muted-foreground">{conversationSummary}</p>
              </div>
            ) : null}
            {logsQuery.error ? (
              <div className="space-y-3 rounded-xl border bg-card p-4">
                <div role="alert">
                  <AlertMessage variant="error">{logsQuery.error.message}</AlertMessage>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    void logsQuery.refetch();
                  }}
                  disabled={logsQuery.isFetching}
                >
                  {t("common.actions.retry")}
                </Button>
              </div>
            ) : null}
            {logsQuery.isPending && !logPage ? (
              <div className="rounded-xl border bg-card py-8">
                <SpinnerBlock />
              </div>
            ) : logPage ? (
              <RecentRequestsTable
                requests={logPage.requests}
                accounts={accounts}
                total={logPage.total}
                visibleColumns={visibleColumns}
                columnWidths={columnWidths}
                onColumnWidthChange={setColumnWidth}
                limit={filters.limit}
                offset={filters.offset}
                hasMore={logPage.hasMore}
                filtersApplied={emptyStateFiltersApplied}
                onLimitChange={(limit) => updateFilters({ limit, offset: 0 })}
                onOffsetChange={(offset) => updateFilters({ offset })}
                onConversationClick={(conversationId) => updateFilters({ conversationId, offset: 0 })}
              />
            ) : null}
          </>
        )}
      </section>
    </div>
  );
}
