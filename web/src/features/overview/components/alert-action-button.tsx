import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type { Alert } from "@/features/overview/schemas";
import { alertToAction, type OverviewAction } from "@/features/overview/components/overview-actions";

/** The one action of an alert (R15). */
export function AlertActionButton({ alert, onAction }: { alert: Alert; onAction: (action: OverviewAction) => void }) {
  const { t } = useTranslation();
  return (
    <Button
      type="button"
      size="sm"
      variant={alert.severity === "critical" ? "default" : "outline"}
      className="h-7 shrink-0 text-xs"
      onClick={() => onAction(alertToAction(alert))}
    >
      {t(`overview.alerts.actions.${alert.action.kind}`)}
    </Button>
  );
}
