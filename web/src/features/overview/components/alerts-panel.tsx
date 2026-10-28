import { AlertOctagon, AlertTriangle, CheckCircle2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { AlertActionButton } from "@/features/overview/components/alert-action-button";
import type { OverviewAction } from "@/features/overview/components/overview-actions";
import { agoLabel, clockLabel, percentLabel, untilLabel } from "@/features/overview/overview-format";
import type { Alert } from "@/features/overview/schemas";
import { cn } from "@/lib/utils";

const VISIBLE_ALERTS = 6;

/** The message of one alert, by rule and severity, or by count for a summary. */
function useAlertMessage() {
  const { t } = useTranslation();
  return (alert: Alert): string => {
    const name = alert.subject.type === "pool" ? t("overview.alerts.pool") : alert.subject.name;
    const value = percentLabel(alert.value);
    const window = alert.window ? t(`overview.windows.${alert.window}`, { defaultValue: alert.window }) : "";
    if (alert.count > 1) {
      return t(`overview.alerts.summary.${alert.rule}.${alert.severity}`, { count: alert.count });
    }
    switch (alert.rule) {
      case "account_reauth":
        return t("overview.alerts.messages.accountReauth", { name });
      case "quota_runway": {
        const until = untilLabel(alert.at);
        return until
          ? t("overview.alerts.messages.runwayExhausts", { name, window, time: clockLabel(alert.at), until })
          : t("overview.alerts.messages.runwayLow", { name, window, value });
      }
      case "key_limit":
        return alert.severity === "critical"
          ? t("overview.alerts.messages.keyLimitBlocked", { name, value })
          : t("overview.alerts.messages.keyLimitNear", { name, value });
      case "error_spike":
        return t("overview.alerts.messages.errorSpike", { value: `${(alert.value ?? 0).toFixed(1)}%` });
      case "key_error_spike":
        return t("overview.alerts.messages.keyErrorSpike", { name, value: `${(alert.value ?? 0).toFixed(1)}%` });
      case "key_expiry":
        return t("overview.alerts.messages.keyExpiry", { name, until: untilLabel(alert.at) ?? t("formatters.now") });
      case "quota_data_stale": {
        const ago = agoLabel(alert.at);
        return ago ? t("overview.alerts.messages.quotaStale", { ago }) : t("overview.alerts.messages.quotaNeverSynced");
      }
    }
  };
}

/** Alerts sorted by the server, the first six shown with "show all" (R13–R15). */
export function AlertsPanel({ alerts, onAction }: { alerts: Alert[]; onAction: (action: OverviewAction) => void }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const message = useAlertMessage();
  if (alerts.length === 0) {
    return (
      <section aria-labelledby="overview-alerts-title" className="flex items-center gap-2 rounded-xl border bg-card px-4 py-3 text-sm">
        <h2 id="overview-alerts-title" className="sr-only">{t("overview.alerts.title")}</h2>
        <CheckCircle2 className="h-4 w-4 text-emerald-500" aria-hidden="true" />
        <span>{t("overview.alerts.none")}</span>
      </section>
    );
  }
  const visible = expanded ? alerts : alerts.slice(0, VISIBLE_ALERTS);
  return (
    <section aria-labelledby="overview-alerts-title" className="rounded-xl border bg-card">
      <div className="flex items-center justify-between border-b px-4 py-2">
        <h2 id="overview-alerts-title" className="text-[13px] font-medium uppercase tracking-wider text-muted-foreground">
          {t("overview.alerts.title")} ({alerts.length})
        </h2>
      </div>
      <ul className="divide-y">
        {visible.map((alert) => {
          const Icon = alert.severity === "critical" ? AlertOctagon : AlertTriangle;
          return (
            <li key={alert.id} data-testid="overview-alert" className="flex items-center gap-3 px-4 py-2 text-sm">
              <Icon
                className={cn("h-4 w-4 shrink-0", alert.severity === "critical" ? "text-red-500" : "text-amber-500")}
                aria-label={t(`overview.severity.${alert.severity}`)}
              />
              <span className="min-w-0 flex-1 truncate">{message(alert)}</span>
              <AlertActionButton alert={alert} onAction={onAction} />
            </li>
          );
        })}
      </ul>
      {alerts.length > VISIBLE_ALERTS ? (
        <div className="border-t px-4 py-2">
          <Button type="button" variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setExpanded((value) => !value)}>
            {expanded ? t("overview.alerts.showFewer") : t("overview.alerts.showAll", { count: alerts.length })}
          </Button>
        </div>
      ) : null}
    </section>
  );
}
