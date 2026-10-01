import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import { MiniQuotaBar } from "@/components/mini-quota-bar";
import type { ApiKey } from "@/features/api-keys/schemas";
import { formatPercentNullable } from "@/utils/formatters";
import { ApiKeyLimitSummary, ApiKeyStatusBadge } from "./api-key-limit-usage";

export type ApiListItemProps = {
  apiKey: ApiKey;
  selected: boolean;
  onSelect: (keyId: string) => void;
};

export function ApiListItem({ apiKey, selected, onSelect }: ApiListItemProps) {
  const { t } = useTranslation();
  const primary = apiKey.pooledRemainingPercentPrimary ?? null;
  const secondary = apiKey.pooledRemainingPercentSecondary ?? null;
  const hasPrimary = apiKey.pooledCapacityCreditsPrimary > 0 && primary !== null;
  const hasSecondary = secondary !== null;
  const visibleRows = Number(hasPrimary) + Number(hasSecondary);

  return (
    <button
      type="button"
      onClick={() => onSelect(apiKey.id)}
      className={cn(
        "w-full rounded-lg px-3 py-2.5 text-left transition-colors",
        selected
          ? "bg-primary/8 ring-1 ring-primary/25"
          : "hover:bg-muted/50",
      )}
    >
      <div className="flex items-center gap-2.5">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">{apiKey.name}</p>
        </div>
        <ApiKeyStatusBadge apiKey={apiKey} />
      </div>
      {visibleRows > 0 ? (
        <div className={cn("mt-2 grid gap-2", visibleRows > 1 ? "grid-cols-2" : "grid-cols-1")}>
          {hasPrimary ? (
            <div className="space-y-1">
              <div className="flex items-center justify-between text-[11px]">
                <span className="text-muted-foreground">{t("apis.listItem.pooled5h")}</span>
                <span className="tabular-nums font-medium">{formatPercentNullable(primary)}</span>
              </div>
              <MiniQuotaBar
                aria-label={t("apis.listItem.pooled5hAria")}
                percent={primary}
                testId="pooled-quota-track-5h"
              />
            </div>
          ) : null}
          {hasSecondary ? (
            <div className="space-y-1">
              <div className="flex items-center justify-between text-[11px]">
                <span className="text-muted-foreground">{t("apis.listItem.pooledWeekly")}</span>
                <span className="tabular-nums font-medium">{formatPercentNullable(secondary)}</span>
              </div>
              <MiniQuotaBar
                aria-label={t("apis.listItem.pooledWeeklyAria")}
                percent={secondary}
                testId="pooled-quota-track-weekly"
              />
            </div>
          ) : null}
        </div>
      ) : null}
      <ApiKeyLimitSummary limits={apiKey.limits} />
    </button>
  );
}
