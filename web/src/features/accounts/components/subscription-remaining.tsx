import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { AccountSubscription } from "@/features/accounts/schemas";
import type { DateDisplayFormat } from "@/hooks/use-date-format";
import { cn } from "@/lib/utils";
import {
  formatDateTimeInline,
  formatSingleUnitRemaining,
  parseDate,
} from "@/utils/formatters";

type SubscriptionRemainingProps = {
  /** End recorded in the ID token at the last sign-in. */
  activeUntil: string | null | undefined;
  /** Last end ChatGPT reported; it wins over the recorded end. */
  subscription?: AccountSubscription | null;
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
  subscription,
  displayFormat,
  className,
}: SubscriptionRemainingProps) {
  const { t } = useTranslation();
  const nowMs = useCurrentTime();
  const live = subscription && parseDate(subscription.activeUntil) ? subscription : null;
  const until = live ? live.activeUntil : activeUntil;
  const endDate = parseDate(until);

  if (!until || !endDate) {
    return (
      <span data-testid="subscription-remaining" className={className}>
        {t("accounts.subscription.unknown")}
      </span>
    );
  }

  const hasEnded = endDate.getTime() <= nowMs;
  // Only a read taken after the end confirms it; an older one may predate a renewal.
  const endConfirmed = live !== null && (parseDate(live.observedAt)?.getTime() ?? 0) >= endDate.getTime();
  const countdown = formatSingleUnitRemaining(until, nowMs);
  const label = hasEnded
    ? t(endConfirmed ? "accounts.subscription.liveEnded" : "accounts.subscription.ended")
    : t("accounts.subscription.remaining", { countdown: countdown.label });
  const offsetDate = new Date(endDate.getTime() + 14 * 86_400_000);
  const offsetUntil = offsetDate.toISOString();
  const offsetLabel = offsetDate.getTime() <= nowMs
    ? t("accounts.subscription.offsetEnded")
    : t("accounts.subscription.offsetRemaining", {
        countdown: formatSingleUnitRemaining(offsetUntil, nowMs).label,
      });
  const dates = {
    date: formatDateTimeInline(until, displayFormat),
    offsetDate: formatDateTimeInline(offsetUntil, displayFormat),
  };
  const title = live
    ? t("accounts.subscription.liveTooltip", {
        ...dates,
        checked: formatDateTimeInline(live.observedAt, displayFormat),
        renewal: t(live.willRenew == null
          ? "accounts.subscription.renewalUnknown"
          : live.willRenew ? "accounts.subscription.renews" : "accounts.subscription.notRenewing"),
      })
    : t("accounts.subscription.tooltip", dates);

  return (
    <span
      data-testid="subscription-remaining"
      className={cn("tabular-nums", className)}
      title={title}
    >
      {label} / {offsetLabel}
    </span>
  );
}
