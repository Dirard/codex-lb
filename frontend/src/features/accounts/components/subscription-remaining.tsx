import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { DateDisplayFormat } from "@/hooks/use-date-format";
import { cn } from "@/lib/utils";
import {
  formatDateTimeInline,
  formatSingleUnitRemaining,
  parseDate,
} from "@/utils/formatters";

type SubscriptionRemainingProps = {
  activeUntil: string | null | undefined;
  displayFormat: DateDisplayFormat;
  className?: string;
};

function useCurrentTime(): number {
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    const intervalId = window.setInterval(() => setNowMs(Date.now()), 60_000);
    return () => window.clearInterval(intervalId);
  }, []);

  return nowMs;
}

export function SubscriptionRemaining({
  activeUntil,
  displayFormat,
  className,
}: SubscriptionRemainingProps) {
  const { t } = useTranslation();
  const nowMs = useCurrentTime();
  const endDate = parseDate(activeUntil);

  if (!activeUntil || !endDate) {
    return (
      <span data-testid="subscription-remaining" className={className}>
        {t("accounts.subscription.unknown")}
      </span>
    );
  }

  const hasEnded = endDate.getTime() <= nowMs;
  const countdown = formatSingleUnitRemaining(activeUntil, nowMs);
  const label = hasEnded
    ? t("accounts.subscription.ended")
    : t("accounts.subscription.remaining", { countdown: countdown.label });
  const title = t("accounts.subscription.tooltip", {
    date: formatDateTimeInline(activeUntil, displayFormat),
  });

  return (
    <span
      data-testid="subscription-remaining"
      className={cn("tabular-nums", className)}
      title={title}
    >
      {label}
    </span>
  );
}
