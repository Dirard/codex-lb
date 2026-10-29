import { CheckCircle2, MoreHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { OverviewAction } from "@/features/overview/components/overview-actions";
import { percentLabel, untilLabel, usdLabel } from "@/features/overview/overview-format";
import type { AttentionKey } from "@/features/overview/schemas";

function primaryAction(key: AttentionKey): OverviewAction {
  switch (key.primaryAction) {
    case "edit_limits":
      return { kind: "edit_limits", target: key.id };
    case "open_group_limits":
      return { kind: "open_group_limits", target: key.groupId ?? "" };
    case "open_key_errors":
      return { kind: "open_key_errors", target: key.id };
    case "extend_expiry":
      return { kind: "extend_expiry", target: key.id, name: key.name, expiresAt: key.expiresAt };
  }
  return { kind: "open_key", target: key.id };
}

/**
 * The ten keys needing attention, ranked by the server (R16): reasons as
 * badges, the primary action inline and a menu with Open, Edit limits,
 * Extend expiry and Disable (R17).
 */
export function AttentionKeysTable({
  keys,
  total,
  onAction,
}: {
  keys: AttentionKey[];
  total: number;
  onAction: (action: OverviewAction) => void;
}) {
  const { t } = useTranslation();
  return (
    <section aria-labelledby="overview-attention-title" className="rounded-xl border bg-card">
      <div className="flex items-center justify-between border-b px-4 py-2">
        <h2 id="overview-attention-title" className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
          {t("overview.attention.title")}
        </h2>
      </div>
      {keys.length === 0 ? (
        <p className="flex items-center gap-2 px-4 py-3 text-sm">
          <CheckCircle2 className="h-4 w-4 text-emerald-500" aria-hidden="true" />
          {t("overview.attention.none")}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-[11px] uppercase tracking-wider text-muted-foreground">
                <th className="px-3 py-2 font-medium">{t("overview.columns.key")}</th>
                <th className="px-3 py-2 font-medium">{t("overview.columns.group")}</th>
                <th className="px-3 py-2 font-medium">{t("overview.columns.reasons")}</th>
                <th className="px-3 py-2 text-right font-medium">{t("overview.columns.spendToday")}</th>
                <th className="px-3 py-2 font-medium">{t("overview.columns.fullestLimit")}</th>
                <th className="px-3 py-2 font-medium">{t("overview.columns.expiry")}</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {keys.map((key) => (
                <tr key={key.id} data-testid="overview-attention-key" className="border-b last:border-0">
                  <td className="px-3 py-2">
                    <Link className="font-medium hover:underline" to={`/keys?selected=${encodeURIComponent(key.id)}`}>
                      {key.name}
                    </Link>
                    <span className="block font-mono text-[11px] text-muted-foreground">{key.prefix}</span>
                  </td>
                  <td className="px-3 py-2 text-xs">{key.groupName ?? t("overview.groups.ungrouped")}</td>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-1">
                      {key.reasons.map((reason) => (
                        <Badge
                          key={reason.reason}
                          variant={reason.severity === "critical" ? "destructive" : "secondary"}
                          className="text-[10px]"
                        >
                          {t(`overview.attention.reasons.${reason.reason}`)}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">{usdLabel(key.spendTodayUsd)}</td>
                  <td className="px-3 py-2 text-xs tabular-nums">
                    {key.maxLimit ? (
                      <>
                        <span className="font-medium">{percentLabel(key.maxLimit.percent)}</span>{" "}
                        <span className="text-muted-foreground">
                          {key.maxLimit.window}
                          {untilLabel(key.maxLimit.resetAt) ? ` · ${t("overview.window.resetsIn", { until: untilLabel(key.maxLimit.resetAt) })}` : ""}
                        </span>
                      </>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-xs">
                    {key.expiresAt ? untilLabel(key.expiresAt) ?? t("common.states.expired") : <span className="text-muted-foreground">—</span>}
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex items-center justify-end gap-1">
                      <Button type="button" size="sm" variant="outline" className="h-7 text-xs" onClick={() => onAction(primaryAction(key))}>
                        {t(`overview.attention.actions.${key.primaryAction}`)}
                      </Button>
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button type="button" size="icon" variant="ghost" className="h-7 w-7" aria-label={t("overview.attention.more", { name: key.name })}>
                            <MoreHorizontal className="h-3.5 w-3.5" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onSelect={() => onAction({ kind: "open_key", target: key.id })}>
                            {t("overview.attention.menu.open")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onSelect={() =>
                              onAction(key.groupId ? { kind: "open_group_limits", target: key.groupId } : { kind: "edit_limits", target: key.id })
                            }
                          >
                            {t("overview.attention.menu.editLimits")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onSelect={() => onAction({ kind: "extend_expiry", target: key.id, name: key.name, expiresAt: key.expiresAt })}
                          >
                            {t("overview.attention.menu.extend")}
                          </DropdownMenuItem>
                          <DropdownMenuItem onSelect={() => onAction({ kind: "disable_key", target: key.id, name: key.name })}>
                            {t("overview.attention.menu.disable")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {total > 0 ? (
        <div className="flex flex-wrap items-center gap-3 border-t px-4 py-2 text-xs text-muted-foreground">
          <span>{t("overview.attention.total", { count: total })}</span>
          <Link className="hover:underline" to="/keys?near_limit=1">
            {t("overview.attention.nearLimitLink")}
          </Link>
          <Link className="hover:underline" to="/keys?expiring_days=7">
            {t("overview.attention.expiringLink")}
          </Link>
        </div>
      ) : null}
    </section>
  );
}
