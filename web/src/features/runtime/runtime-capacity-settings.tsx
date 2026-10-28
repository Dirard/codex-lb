import { useQuery } from "@tanstack/react-query";
import { Gauge } from "lucide-react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { getRuntimeCapacity } from "@/features/runtime/api";
import type { RuntimeCapacity } from "@/features/runtime/schemas";
import { ApiError } from "@/lib/api-client";
import { formatBytes, formatNumber } from "@/utils/formatters";
import { getErrorMessageOrNull } from "@/utils/errors";

const CAPACITY_QUERY_KEY = ["runtime", "capacity"] as const;

function formatBodyBytes(bytes: number): string {
  return bytes > 0 ? formatBytes(bytes) : "0 B";
}

function UsageRow({
  label,
  used,
  limit,
  format = formatNumber,
}: {
  label: string;
  used: number;
  limit: number;
  format?: (value: number) => string;
}) {
  const percent = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0;
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular-nums">{format(used)} / {format(limit)}</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-muted" role="presentation">
        <div className="h-full rounded-full bg-primary" style={{ width: `${percent}%` }} />
      </div>
    </div>
  );
}

function CapacityDetails({ capacity }: { capacity: RuntimeCapacity }) {
  const { t } = useTranslation();
  const { host, limits, usage } = capacity;
  const source = t(`settings.capacity.sources.${host.memorySource}`, { defaultValue: host.memorySource });
  return (
    <div className="mt-4 space-y-4 text-sm">
      <p>
        {t("settings.capacity.host", { cpus: host.cpus, memory: formatBytes(host.memoryBytes), source })}
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        <UsageRow label={t("settings.capacity.generations")} used={usage.activeGenerations} limit={limits.maxGenerations} />
        <UsageRow label={t("settings.capacity.queue")} used={usage.queuedGenerations} limit={limits.maxQueued} />
        <UsageRow label={t("settings.capacity.websockets")} used={usage.clientWebSockets} limit={limits.clientWebSockets} />
        <UsageRow
          label={t("settings.capacity.memory")}
          used={Math.round(usage.residentBytes / (1 << 20))}
          limit={Math.round((limits.goMemoryLimitBytes || host.memoryBytes) / (1 << 20))}
        />
        <UsageRow label={t("settings.capacity.bodies")} used={usage.bodyBytes} limit={limits.bodyBudgetBytes} format={formatBodyBytes} />
      </div>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <dt>{t("settings.capacity.goMemoryLimit")}</dt>
        <dd className="tabular-nums text-foreground">{limits.goMemoryLimitBytes > 0 ? formatBytes(limits.goMemoryLimitBytes) : t("settings.capacity.unset")}</dd>
        <dt>{t("settings.capacity.bodyKeyLimit")}</dt>
        <dd className="tabular-nums text-foreground">{formatBytes(limits.bodyBudgetKeyBytes)}</dd>
        <dt>{t("settings.capacity.bodyRejections")}</dt>
        <dd className="tabular-nums text-foreground">{formatNumber(usage.bodyRejections)}</dd>
        <dt>{t("settings.capacity.goHeap")}</dt>
        <dd className="tabular-nums text-foreground">{formatBytes(usage.goHeapBytes)}</dd>
        <dt>{t("settings.capacity.upstream")}</dt>
        <dd className="tabular-nums text-foreground">{formatNumber(limits.upstreamWebSockets)}</dd>
        <dt>{t("settings.capacity.sqliteReaders")}</dt>
        <dd className="tabular-nums text-foreground">{formatNumber(limits.sqliteReaders)}</dd>
      </dl>
      {capacity.overridden.length > 0 ? (
        <p className="text-xs text-muted-foreground">
          {t("settings.capacity.overridden", {
            names: capacity.overridden.map((name) => t(`settings.capacity.overrides.${name}`, { defaultValue: name })).join(", "),
          })}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">{t("settings.capacity.automatic")}</p>
      )}
    </div>
  );
}

export function RuntimeCapacitySettings() {
  const { t } = useTranslation();
  const query = useQuery({
    queryKey: CAPACITY_QUERY_KEY,
    queryFn: getRuntimeCapacity,
    retry: false,
    refetchInterval: (state) => (state.state.error instanceof ApiError && state.state.error.status === 401 ? false : 15_000),
    refetchIntervalInBackground: false,
  });
  // Installations without a capacity plan (older servers) answer 404; hide the card.
  if (query.error instanceof ApiError && query.error.status === 404) return null;
  const loadError = query.isError ? getErrorMessageOrNull(query.error, t("settings.capacity.loadFailed")) : null;
  return (
    <section className="rounded-xl border bg-card p-5" aria-labelledby="runtime-capacity-title">
      <div className="flex items-start gap-2.5">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
          <Gauge className="h-4 w-4 text-primary" aria-hidden="true" />
        </div>
        <div>
          <h3 id="runtime-capacity-title" className="text-sm font-semibold">{t("settings.capacity.title")}</h3>
          <p className="text-xs text-muted-foreground">{t("settings.capacity.description")}</p>
        </div>
      </div>
      {query.data ? <CapacityDetails capacity={query.data} /> : null}
      {query.isPending ? <p className="mt-4 text-sm text-muted-foreground">{t("settings.capacity.loading")}</p> : null}
      {loadError ? <div className="mt-3" role="alert"><AlertMessage variant="error">{loadError}</AlertMessage></div> : null}
    </section>
  );
}
