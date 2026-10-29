import { Plus, Search, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  DEFAULT_KEYS_PARAMS,
  EXPIRING_DAYS,
  hasKeyFilters,
  INACTIVE_DAY_CHOICES,
  type KeysListParams,
} from "@/features/keys/api";
import type { UpdateKeysParams } from "@/features/keys/hooks/use-keys-list";
import { KEY_STATUSES, type KeyStatus } from "@/features/keys/schemas";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { cn } from "@/lib/utils";

const ANY = "__any__";
const UNGROUPED = "ungrouped";
const SEARCH_DEBOUNCE_MS = 300;

function ToggleChip({ pressed, onClick, children }: { pressed: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <Button
      type="button"
      size="sm"
      variant={pressed ? "default" : "outline"}
      aria-pressed={pressed}
      className={cn("h-8 text-xs", !pressed && "text-muted-foreground")}
      onClick={onClick}
    >
      {children}
    </Button>
  );
}

/**
 * The keys toolbar: search by name or prefix, group, status, near limit,
 * expiring within 7 days and inactive for 7, 30 or 90 days, all kept in the
 * URL; Create key and the number of matching keys. The toolbar stays usable
 * while the table loads.
 */
export function KeysFilters({
  params,
  total,
  onChange,
  onCreate,
}: {
  params: KeysListParams;
  total: number | null;
  onChange: UpdateKeysParams;
  onCreate: () => void;
}) {
  const { t } = useTranslation();
  const directory = useDirectory();
  const groups = directory.data?.groups ?? [];
  const [text, setText] = useState(params.q);
  const lastSent = useRef(params.q);

  // A search typed here reaches the URL after a short pause; a URL change
  // from elsewhere (back, reset) replaces the field.
  useEffect(() => {
    if (params.q !== lastSent.current) {
      lastSent.current = params.q;
      setText(params.q);
    }
  }, [params.q]);
  useEffect(() => {
    if (text === lastSent.current) return;
    const timer = window.setTimeout(() => {
      lastSent.current = text;
      onChange({ q: text }, { replace: true });
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [text, onChange]);

  const groupValue = params.group ?? ANY;
  const knownGroup = groupValue === ANY || groupValue === UNGROUPED || groups.some((group) => group.id === groupValue);

  return (
    <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label={t("keys.filters.label")}>
      <div className="relative w-full sm:w-64">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
        <Input
          value={text}
          onChange={(event) => setText(event.target.value)}
          placeholder={t("keys.filters.search")}
          aria-label={t("keys.filters.search")}
          maxLength={256}
          className="h-8 pl-8 text-xs"
        />
      </div>
      <Select value={groupValue} onValueChange={(value) => onChange({ group: value === ANY ? null : value })}>
        <SelectTrigger className="h-8 w-44 text-xs" aria-label={t("keys.filters.group")}>
          <SelectValue placeholder={t("keys.filters.anyGroup")} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>{t("keys.filters.anyGroup")}</SelectItem>
          <SelectItem value={UNGROUPED}>{t("keys.filters.ungrouped")}</SelectItem>
          {!knownGroup ? <SelectItem value={groupValue}>{groupValue}</SelectItem> : null}
          {groups.map((group) => (
            <SelectItem key={group.id} value={group.id}>
              {group.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={params.status ?? ANY}
        onValueChange={(value) => onChange({ status: value === ANY ? null : (value as KeyStatus) })}
      >
        <SelectTrigger className="h-8 w-32 text-xs" aria-label={t("keys.filters.status")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>{t("keys.filters.anyStatus")}</SelectItem>
          {KEY_STATUSES.map((status) => (
            <SelectItem key={status} value={status}>
              {t(`common.states.${status}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <ToggleChip pressed={params.nearLimit} onClick={() => onChange({ nearLimit: !params.nearLimit })}>
        {t("keys.filters.nearLimit")}
      </ToggleChip>
      <ToggleChip
        pressed={params.expiringDays !== null}
        onClick={() => onChange({ expiringDays: params.expiringDays === null ? EXPIRING_DAYS : null })}
      >
        {t("keys.filters.expiringWithin", { count: params.expiringDays ?? EXPIRING_DAYS })}
      </ToggleChip>
      <Select
        value={params.inactiveDays === null ? ANY : String(params.inactiveDays)}
        onValueChange={(value) => onChange({ inactiveDays: value === ANY ? null : Number(value) })}
      >
        <SelectTrigger className="h-8 w-40 text-xs" aria-label={t("keys.filters.inactive")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>{t("keys.filters.anyActivity")}</SelectItem>
          {params.inactiveDays !== null && !(INACTIVE_DAY_CHOICES as readonly number[]).includes(params.inactiveDays) ? (
            <SelectItem value={String(params.inactiveDays)}>{t("keys.filters.inactiveFor", { count: params.inactiveDays })}</SelectItem>
          ) : null}
          {INACTIVE_DAY_CHOICES.map((days) => (
            <SelectItem key={days} value={String(days)}>
              {t("keys.filters.inactiveFor", { count: days })}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {hasKeyFilters(params) ? (
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-8 gap-1 text-xs"
          onClick={() => {
            lastSent.current = "";
            setText("");
            onChange({ ...DEFAULT_KEYS_PARAMS, sort: params.sort, order: params.order });
          }}
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
          {t("keys.filters.reset")}
        </Button>
      ) : null}
      <div className="ml-auto flex items-center gap-3">
        {total !== null ? (
          <span className="text-xs tabular-nums text-muted-foreground" data-testid="keys-total">
            {t("keys.filters.total", { count: total })}
          </span>
        ) : null}
        <Button type="button" size="sm" className="h-8 gap-1.5 text-xs" onClick={onCreate}>
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          {t("keys.filters.create")}
        </Button>
      </div>
    </div>
  );
}
