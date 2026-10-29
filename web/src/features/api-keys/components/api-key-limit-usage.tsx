import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import type { ApiKey } from "@/features/api-keys/schemas";
import { cn } from "@/lib/utils";
import { formatPercentNullable } from "@/utils/formatters";

function isExpired(expiresAt: string | null): boolean {
  return expiresAt !== null && new Date(expiresAt).getTime() <= Date.now();
}

export function ApiKeyStatusBadge({ apiKey }: { apiKey: Pick<ApiKey, "isActive" | "expiresAt"> }) {
  const { t } = useTranslation();
  const expired = isExpired(apiKey.expiresAt);
  return (
    <Badge className={cn(!apiKey.isActive || expired ? "bg-zinc-500 text-white" : "bg-emerald-500 text-white")}>
      {!apiKey.isActive ? t("common.states.disabled") : expired ? t("common.states.expired") : t("common.states.active")}
    </Badge>
  );
}

export function LimitUsageBar({ percent, label }: { percent: number; label: string }) {
  const clamped = Math.max(0, Math.min(100, percent));
  return (
    <div role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={clamped}
      className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
      <div className={cn("h-full rounded-full", clamped >= 90 ? "bg-red-500" : clamped >= 70 ? "bg-orange-500" : clamped >= 40 ? "bg-amber-500" : "bg-emerald-500")}
        style={{ width: `${clamped}%` }} />
    </div>
  );
}

// The key list summarizes the most consumed of this key's independent limits.
export function ApiKeyLimitSummary({ limits }: { limits: Pick<ApiKey["limits"][number], "maxValue" | "currentValue">[] }) {
  const { t } = useTranslation();
  if (limits.length === 0) return null;
  const percent = limits.reduce((highest, limit) => limit.maxValue > 0
    ? Math.max(highest, limit.currentValue / limit.maxValue * 100) : highest, 0);
  const label = t("apis.listItem.apiLimit");
  return (
    <div className="mt-1.5 space-y-1">
      <div className="flex items-center justify-between text-[11px]">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular-nums font-medium">{formatPercentNullable(percent)}</span>
      </div>
      <LimitUsageBar percent={percent} label={label} />
    </div>
  );
}
