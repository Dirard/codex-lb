import { useInfiniteQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import {
  fetchAdminKeyRequests,
  fetchOwnerKeyRequests,
  keyRequestsQueryKey,
  type KeyRequestsQuery,
} from "@/features/key-page/api";
import { AccountLabel } from "@/features/key-page/components/key-admin-sections";
import { SectionCard, TableHeadRow, Th } from "@/features/key-page/components/section-card";
import { isUnauthorized } from "@/features/key-page/hooks/use-key-page";
import { countLabel, dateTimeLabel, DASH, msLabel, usd } from "@/features/key-page/key-page-format";
import type { KeyPageMode, KeyRequestRow, KeyRequestsResponse } from "@/features/key-page/schemas";
import type { Directory } from "@/features/search/api";
import { cn } from "@/lib/utils";
import { getErrorMessageOrNull } from "@/utils/errors";
import { formatModelLabel } from "@/utils/formatters";

const TRANSPORT_LABELS: Record<string, string> = { http: "HTTP", websocket: "WS", auto: "Auto", automation: "Automation" };

/** A status that counts as an error: anything but success, completed and cancelled. */
function failed(status: string): boolean {
  return !["success", "completed", "cancelled", "ok"].includes(status);
}

function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation();
  return (
    <span
      className={cn(
        "inline-flex rounded border px-1.5 py-px text-[11px] font-medium",
        status === "cancelled"
          ? "border-sky-500/20 bg-sky-500/15 text-sky-700 dark:text-sky-400"
          : failed(status)
            ? "border-red-500/20 bg-red-500/15 text-red-700 dark:text-red-400"
            : "border-emerald-500/20 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
      )}
    >
      {t(`dashboard.requestStatus.${status}`, { defaultValue: status })}
    </span>
  );
}

function Field({ label, value, mono = false, copy }: { label: string; value: string | null | undefined; mono?: boolean; copy?: string }) {
  return (
    <div className="min-w-0 space-y-0.5">
      <div className="flex items-center gap-1.5">
        <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</p>
        {copy ? <CopyButton value={copy} label={label} iconOnly /> : null}
      </div>
      <p className={cn("break-all text-sm", mono && "font-mono text-xs")}>{value || DASH}</p>
    </div>
  );
}

/** Administrator: every stored field of one request. */
function RequestDetailsDialog({
  request,
  directory,
  onClose,
}: {
  request: KeyRequestRow;
  directory: Directory | undefined;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("dashboard.requestDetails.title")}</DialogTitle>
          <DialogDescription>{t("dashboard.requestDetails.description")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 rounded-md border bg-muted/30 p-4 sm:grid-cols-3">
          <div className="sm:col-span-3">
            <Field label={t("dashboard.requestDetails.requestId")} value={request.requestId} mono copy={request.requestId} />
          </div>
          <Field label={t("keyPage.columns.time")} value={dateTimeLabel(request.requestedAt)} />
          <Field label={t("keyPage.columns.status")} value={t(`dashboard.requestStatus.${request.status}`, { defaultValue: request.status })} />
          <Field label={t("dashboard.requestDetails.requestKind")} value={request.requestKind} />
          <Field label={t("keyPage.columns.model")} value={formatModelLabel(request.model, request.reasoningEffort, request.serviceTier)} mono />
          <Field label={t("dashboard.requests.columns.plan")} value={request.planType} />
          <Field label={t("keyPage.columns.ttft")} value={msLabel(request.latencyFirstTokenMs)} />
          <Field label={t("keyPage.columns.latency")} value={msLabel(request.latencyMs)} />
          <Field label={t("dashboard.requestDetails.queue")} value={msLabel(request.latencyQueueMs)} />
          <Field label={t("common.units.input")} value={countLabel(request.inputTokens)} />
          <Field label={t("common.units.output")} value={countLabel(request.outputTokens)} />
          <Field label={t("common.units.cached")} value={countLabel(request.cachedInputTokens)} />
          <Field label={t("keyPage.columns.cost")} value={usd(request.costUsd)} />
          <Field label={t("keyPage.columns.transport")} value={request.transport} />
          <Field label={t("keyPage.columns.source")} value={request.source ?? request.modelSourceKind} />
          <Field label={t("dashboard.requestDetails.clientIp")} value={request.clientIp} copy={request.clientIp ?? undefined} />
          <div className="sm:col-span-2">
            <Field label={t("dashboard.requestDetails.conversationId")} value={request.conversationId} mono copy={request.conversationId ?? undefined} />
          </div>
          <div className="sm:col-span-3">
            <Field label={t("dashboard.requestDetails.userAgent")} value={request.useragent ?? request.useragentGroup} />
          </div>
          <div className="sm:col-span-3">
            <Field label={t("dashboard.requestDetails.errorCode")} value={request.errorCode} mono />
          </div>
        </div>
        <div className="space-y-1">
          <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{t("keyPage.columns.account")}</p>
          <p className="text-sm"><AccountLabel accountId={request.accountId} directory={directory} /></p>
        </div>
        <div className="space-y-1">
          <h3 className="text-sm font-medium">{t("dashboard.requestDetails.fullError")}</h3>
          <p className="max-h-[30vh] overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-muted/50 p-3 font-mono text-xs">
            {request.errorMessage ?? request.errorCode ?? t("dashboard.requestDetails.noErrorDetail")}
          </p>
        </div>
        <DialogFooter showCloseButton />
      </DialogContent>
    </Dialog>
  );
}

function dedupe(rows: KeyRequestRow[]): KeyRequestRow[] {
  const seen = new Set<string>();
  return rows.filter((row) => {
    const id = `${row.requestId}:${row.requestedAt}`;
    if (seen.has(id)) return false;
    seen.add(id);
    return true;
  });
}

/**
 * The key's recent requests, newest first: the first page comes with the key
 * page, "load more" follows `nextCursor` through the requests endpoint and is
 * never polled; "errors only" reads failed requests from the start (R23).
 * The administrator adds the account, client IP, conversation and details.
 */
export function KeyRequestsSection({
  mode,
  keyId,
  firstPage,
  firstCursor,
  errorsRun,
  onErrorsOnlyChange,
  onUnauthorized,
  directory,
}: {
  mode: KeyPageMode;
  keyId: string | undefined;
  firstPage: KeyRequestRow[];
  firstCursor: string | null;
  /** Null for all requests; each switch to errors only gets a new run read from the newest request. */
  errorsRun: number | null;
  onErrorsOnlyChange: (errorsOnly: boolean) => void;
  onUnauthorized: () => void;
  directory: Directory | undefined;
}) {
  const { t } = useTranslation();
  const [anchor, setAnchor] = useState<string | null>(null);
  const errorsOnly = errorsRun !== null;
  const [selected, setSelected] = useState<KeyRequestRow | null>(null);
  const base = keyRequestsQueryKey(mode, keyId);
  const load = (query: KeyRequestsQuery, signal: AbortSignal): Promise<KeyRequestsResponse> =>
    mode === "admin" ? fetchAdminKeyRequests(keyId ?? "", query, signal) : fetchOwnerKeyRequests(query, signal);
  const pageOptions = {
    getNextPageParam: (last: KeyRequestsResponse) => last.nextCursor ?? undefined,
    refetchInterval: false as const,
    refetchOnWindowFocus: false,
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  };

  // Pages after the page response's first page, from the cursor of the first click.
  const more = useInfiniteQuery({
    ...pageOptions,
    queryKey: [...base, "more", anchor],
    initialPageParam: anchor,
    queryFn: ({ pageParam, signal }) => load({ cursor: pageParam, errorsOnly: false }, signal),
    enabled: !errorsOnly && anchor !== null,
  });
  // Failed requests only, from the newest request; each call scans a bounded number of rows.
  const errors = useInfiniteQuery({
    ...pageOptions,
    queryKey: [...base, "errors", errorsRun],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) => load({ cursor: pageParam, errorsOnly: true }, signal),
    enabled: errorsOnly,
    gcTime: 0,
  });

  const active = errorsOnly ? errors : more;
  const unauthorized = isUnauthorized(active.error);
  useEffect(() => {
    if (unauthorized) onUnauthorized();
  }, [unauthorized, onUnauthorized]);

  const rows = useMemo(() => {
    if (errorsOnly) return dedupe(errors.data?.pages.flatMap((page) => page.items) ?? []);
    return dedupe([...firstPage, ...(more.data?.pages.flatMap((page) => page.items) ?? [])]);
  }, [errorsOnly, errors.data, firstPage, more.data]);

  const hasMore = errorsOnly
    ? errors.hasNextPage
    : anchor === null
      ? firstCursor !== null
      : more.isFetching || more.hasNextPage;
  const loading = errorsOnly ? errors.isFetching : more.isFetching;
  const loadMore = () => {
    if (errorsOnly) {
      void errors.fetchNextPage();
    } else if (anchor === null) {
      setAnchor(firstCursor);
    } else {
      void more.fetchNextPage();
    }
  };
  const errorMessage = active.error && !unauthorized ? getErrorMessageOrNull(active.error) ?? t("keyPage.loadFailed") : null;
  const admin = mode === "admin";

  return (
    <SectionCard
      id="key-page-requests"
      title={t("keyPage.requests.title")}
      actions={
        <label className="flex items-center gap-2 text-xs">
          <Switch
            size="sm"
            checked={errorsOnly}
            onCheckedChange={onErrorsOnlyChange}
            aria-label={t("keyPage.requests.errorsOnly")}
          />
          {t("keyPage.requests.errorsOnly")}
        </label>
      }
    >
      {errorMessage ? (
        <div className="flex flex-wrap items-center gap-3 px-4 pt-3" role="alert">
          <AlertMessage variant="error">{errorMessage}</AlertMessage>
          <Button type="button" variant="outline" size="sm" className="h-7 text-xs" onClick={() => void active.refetch()}>
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : null}
      {rows.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">
          {errorsOnly
            ? errors.isPending
              ? t("keyPage.requests.searching")
              : hasMore
                ? t("keyPage.requests.noErrorsYet")
                : t("keyPage.requests.noErrors")
            : t("keyPage.requests.none")}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full whitespace-nowrap text-xs">
            <thead>
              <TableHeadRow>
                <Th>{t("keyPage.columns.time")}</Th>
                <Th>{t("keyPage.columns.model")}</Th>
                <Th>{t("keyPage.columns.status")}</Th>
                <Th>{t("keyPage.columns.code")}</Th>
                <Th align="right">{t("common.units.input")}</Th>
                <Th align="right">{t("common.units.output")}</Th>
                <Th align="right">{t("common.units.cached")}</Th>
                <Th align="right">{t("keyPage.columns.cost")}</Th>
                <Th align="right">{t("keyPage.columns.ttft")}</Th>
                <Th align="right">{t("keyPage.columns.latency")}</Th>
                <Th>{t("keyPage.columns.transport")}</Th>
                <Th>{t("keyPage.columns.userAgent")}</Th>
                {admin ? (
                  <>
                    <Th>{t("keyPage.columns.account")}</Th>
                    <Th>{t("keyPage.columns.clientIp")}</Th>
                    <Th>{t("keyPage.columns.conversation")}</Th>
                    <Th />
                  </>
                ) : null}
              </TableHeadRow>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={`${row.requestId}:${row.requestedAt}`} className="border-b last:border-0" data-testid="key-request-row">
                  <td className="px-3 py-1.5 tabular-nums" title={row.requestId}>{dateTimeLabel(row.requestedAt)}</td>
                  <td className="max-w-56 truncate px-3 py-1.5 font-mono" title={row.model}>
                    {formatModelLabel(row.model, row.reasoningEffort)}
                  </td>
                  <td className="px-3 py-1.5"><StatusBadge status={row.status} /></td>
                  <td className="px-3 py-1.5 font-mono">{row.errorCode ?? DASH}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{countLabel(row.inputTokens)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{countLabel(row.outputTokens)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{countLabel(row.cachedInputTokens)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{usd(row.costUsd)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(row.latencyFirstTokenMs)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{msLabel(row.latencyMs)}</td>
                  <td className="px-3 py-1.5">{row.transport ? TRANSPORT_LABELS[row.transport] ?? row.transport : DASH}</td>
                  <td className="max-w-40 truncate px-3 py-1.5" title={row.useragentGroup ?? undefined}>{row.useragentGroup ?? DASH}</td>
                  {admin ? (
                    <>
                      <td className="max-w-48 px-3 py-1.5"><AccountLabel accountId={row.accountId} directory={directory} /></td>
                      <td className="px-3 py-1.5 font-mono">{row.clientIp ?? DASH}</td>
                      <td className="max-w-32 truncate px-3 py-1.5 font-mono" title={row.conversationId ?? undefined}>
                        {row.conversationId ?? DASH}
                      </td>
                      <td className="px-3 py-1.5">
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-6 px-2 text-[11px]"
                          aria-label={t("keyPage.requests.details", { id: row.requestId })}
                          onClick={() => setSelected(row)}
                        >
                          {t("common.actions.details")}
                        </Button>
                      </td>
                    </>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {hasMore ? (
        <div className="flex justify-center border-t px-4 py-2">
          <Button type="button" size="sm" variant="outline" className="h-7 text-xs" disabled={loading} onClick={loadMore}>
            {loading ? t("common.loading") : t("keyPage.requests.loadMore")}
          </Button>
        </div>
      ) : null}
      {admin && selected ? <RequestDetailsDialog request={selected} directory={directory} onClose={() => setSelected(null)} /> : null}
    </SectionCard>
  );
}
