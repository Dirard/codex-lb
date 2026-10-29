import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { percentLabel } from "@/features/overview/overview-format";
import type { GroupAccount } from "@/features/groups/schemas";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { cn } from "@/lib/utils";

const HEALTH_DOT: Record<GroupAccount["health"], string> = {
  ok: "bg-emerald-500",
  low: "bg-amber-400",
  limited: "bg-orange-500",
  reauth: "bg-red-500",
  paused: "bg-slate-400",
  off: "bg-slate-600",
};

/**
 * The members of a group as a filterable checklist of every non-deleted
 * account with its status and remaining; members come first.
 */
export function GroupMembersSelect({
  accounts,
  value,
  onChange,
  disabled = false,
}: {
  accounts: GroupAccount[];
  value: string[];
  onChange: (value: string[]) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const blurred = usePrivacyStore((s) => s.blurred);
  const [filter, setFilter] = useState("");
  const selected = useMemo(() => new Set(value), [value]);
  // Order once per opened sheet so rows do not jump while ticking boxes.
  const [initialMembers] = useState(() => new Set(value));
  const listed = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return accounts
      .filter((account) => !needle || [account.label, account.email, account.id].some((text) => text.toLowerCase().includes(needle)))
      .sort((a, b) => Number(initialMembers.has(b.id)) - Number(initialMembers.has(a.id)) || a.label.localeCompare(b.label));
  }, [accounts, filter, initialMembers]);
  const toggle = (id: string, checked: boolean) => {
    onChange(checked ? [...value, id] : value.filter((member) => member !== id));
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <Input
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder={t("groups.sheet.membersFilter")}
          aria-label={t("groups.sheet.membersFilter")}
          className="h-8 text-xs"
        />
        <span className="shrink-0 text-xs text-muted-foreground">{t("groups.sheet.membersSelected", { count: value.length })}</span>
      </div>
      <ul className="max-h-64 divide-y overflow-y-auto rounded-lg border" aria-label={t("groups.sheet.members")}>
        {listed.length === 0 ? <li className="p-3 text-xs text-muted-foreground">{t("groups.sheet.noAccounts")}</li> : null}
        {listed.map((account) => {
          const id = `group-member-${account.id}`;
          return (
            <li key={account.id} className="flex items-center gap-2 px-3 py-1.5 text-xs">
              <Checkbox
                id={id}
                checked={selected.has(account.id)}
                disabled={disabled}
                onCheckedChange={(checked) => toggle(account.id, checked === true)}
              />
              <label htmlFor={id} className="flex min-w-0 flex-1 cursor-pointer items-center gap-2">
                <span className={cn("h-2 w-2 shrink-0 rounded-full", HEALTH_DOT[account.health])} aria-hidden="true" />
                <span className={cn("min-w-0 flex-1 truncate font-medium", blurred && account.label.includes("@") && "privacy-blur")}>
                  {account.label}
                </span>
                <span className="shrink-0 text-muted-foreground" title={account.status}>{t(`overview.health.${account.health}`)}</span>
                {account.kind === "chatgpt" ? (
                  <span className="w-24 shrink-0 text-right tabular-nums text-muted-foreground">
                    {t("groups.sheet.remaining", {
                      primary: percentLabel(account.primaryRemainingPercent),
                      weekly: percentLabel(account.secondaryRemainingPercent),
                    })}
                  </span>
                ) : (
                  <span className="w-24 shrink-0 text-right text-muted-foreground">{t("groups.sheet.external")}</span>
                )}
              </label>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
