import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { dateTimeLabel } from "@/features/key-page/key-page-format";
import type { KeyPageKey, KeyPageMode } from "@/features/key-page/schemas";
import { cn } from "@/lib/utils";
import { formatAgo, formatResetRelative, parseDate } from "@/utils/formatters";

/** What the header shows at once: the directory entry before the page loads. */
export type KeyIdentity = {
  name: string;
  prefix: string;
  status: KeyPageKey["status"];
  groupId: string | null;
  groupName: string | null;
};

const STATUS_CLASS: Record<KeyPageKey["status"], string> = {
  active: "bg-emerald-500 text-white",
  disabled: "bg-zinc-500 text-white",
  expired: "bg-amber-600 text-white",
};

export function KeyStatusBadge({ status }: { status: KeyPageKey["status"] }) {
  const { t } = useTranslation();
  return <Badge className={STATUS_CLASS[status]}>{t(`common.states.${status}`)}</Badge>;
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</dt>
      <dd className="truncate text-xs tabular-nums">{children}</dd>
    </div>
  );
}

function ExpiryCountdown({ expiresAt, now }: { expiresAt: string | null; now: number }) {
  const { t } = useTranslation();
  const date = parseDate(expiresAt);
  if (!date) return <>{t("keyPage.header.neverExpires")}</>;
  const remaining = date.getTime() - now;
  return (
    <time dateTime={date.toISOString()} title={dateTimeLabel(expiresAt)} className={cn(remaining <= 0 && "text-red-600 dark:text-red-400")}>
      {remaining <= 0 ? t("common.states.expired") : formatResetRelative(remaining)}
    </time>
  );
}

/**
 * Name, prefix, status, group, created, last used and the expiry countdown;
 * the administrator's group links to its settings. Shown from the directory
 * entry while the page loads.
 */
export function KeyPageHeader({
  mode,
  identity,
  details,
  now,
  actions,
}: {
  mode: KeyPageMode;
  identity: KeyIdentity | null;
  details: KeyPageKey | null;
  now: number;
  actions: ReactNode;
}) {
  const { t } = useTranslation();
  const lastUsed = parseDate(details?.lastUsedAt);
  return (
    <header className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
            {mode === "admin" ? (
              <Link className="hover:underline" to="/keys">{t("keyPage.header.keys")}</Link>
            ) : (
              t("keyPage.header.ownerTitle")
            )}
          </p>
          {identity ? (
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <h1 className="truncate text-2xl font-semibold tracking-tight" title={identity.name}>{identity.name}</h1>
              <span className="font-mono text-xs text-muted-foreground">{identity.prefix}</span>
              <KeyStatusBadge status={identity.status} />
            </div>
          ) : (
            <Skeleton className="mt-1 h-8 w-64" data-testid="key-page-header-skeleton" />
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      </div>
      <dl className="grid grid-cols-2 gap-3 rounded-xl border bg-card px-4 py-3 sm:grid-cols-4">
        <Fact label={t("keyPage.header.group")}>
          {identity?.groupId ? (
            mode === "admin" ? (
              <Link className="font-medium hover:underline" to={`/settings?group=${encodeURIComponent(identity.groupId)}#account-groups`}>
                {identity.groupName ?? identity.groupId}
              </Link>
            ) : (
              <span className="font-medium">{identity.groupName ?? "—"}</span>
            )
          ) : identity ? (
            <span className="text-muted-foreground">{t("keyPage.header.ungrouped")}</span>
          ) : (
            "—"
          )}
        </Fact>
        <Fact label={t("keyPage.header.created")}>{details ? dateTimeLabel(details.createdAt) : "—"}</Fact>
        <Fact label={t("keyPage.header.lastUsed")}>
          {details ? (
            lastUsed ? (
              <time dateTime={lastUsed.toISOString()} title={dateTimeLabel(details.lastUsedAt)}>
                {formatAgo(now - lastUsed.getTime())}
              </time>
            ) : (
              t("common.time.never")
            )
          ) : (
            "—"
          )}
        </Fact>
        <Fact label={t("keyPage.header.expires")}>
          {details ? <ExpiryCountdown expiresAt={details.expiresAt} now={now} /> : "—"}
        </Fact>
      </dl>
    </header>
  );
}
