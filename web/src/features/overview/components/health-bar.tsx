import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import type { HealthCounts } from "@/features/overview/schemas";

const SEGMENTS: { key: keyof HealthCounts; className: string }[] = [
  { key: "ok", className: "bg-emerald-500" },
  { key: "low", className: "bg-amber-400" },
  { key: "limited", className: "bg-orange-500" },
  { key: "reauth", className: "bg-red-500" },
  { key: "paused", className: "bg-slate-400" },
  { key: "off", className: "bg-slate-600" },
];

/** Account health as a segmented bar with counts by state. */
export function HealthBar({ counts, className }: { counts: HealthCounts; className?: string }) {
  const { t } = useTranslation();
  const total = SEGMENTS.reduce((sum, segment) => sum + counts[segment.key], 0);
  const summary = SEGMENTS.filter((segment) => counts[segment.key] > 0)
    .map((segment) => `${counts[segment.key]} ${t(`overview.health.${segment.key}`)}`)
    .join(" · ");
  return (
    <div className={cn("min-w-[7rem] space-y-1", className)} title={summary || t("overview.health.none")}>
      <div className="flex h-1.5 w-full overflow-hidden rounded-full bg-muted" role="img" aria-label={summary || t("overview.health.none")}>
        {total > 0
          ? SEGMENTS.filter((segment) => counts[segment.key] > 0).map((segment) => (
              <span
                key={segment.key}
                className={segment.className}
                style={{ width: `${(counts[segment.key] / total) * 100}%` }}
              />
            ))
          : null}
      </div>
      <p className="truncate text-[11px] text-muted-foreground">{summary || t("overview.health.none")}</p>
    </div>
  );
}
