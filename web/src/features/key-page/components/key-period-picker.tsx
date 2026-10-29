import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  isValidPeriod,
  KEY_PAGE_MAX_DAYS,
  KEY_PAGE_PRESETS,
  localDateISO,
  periodDays,
  presetOf,
  presetPeriod,
  type KeyPagePeriod,
} from "@/features/key-page/period";
import { cn } from "@/lib/utils";

/**
 * The key page's only period control (R24): Today, 7, 30 and 90 days ending
 * today, or custom days up to 730; the page writes it to `?from=&to=`.
 */
export function KeyPeriodPicker({
  period,
  timezone,
  onChange,
}: {
  period: KeyPagePeriod;
  timezone: string;
  onChange: (period: KeyPagePeriod) => void;
}) {
  const { t } = useTranslation();
  const preset = presetOf(period);
  const [customOpen, setCustomOpen] = useState(false);
  const [draft, setDraft] = useState(period);
  const today = localDateISO();
  const draftValid = isValidPeriod(draft);
  const showCustom = customOpen || preset === "custom";

  const apply = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!draftValid) return;
    onChange(draft);
  };

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl border bg-card px-3 py-2">
      <div role="group" aria-label={t("keyPage.period.label")} className="flex flex-wrap items-center gap-1">
        {KEY_PAGE_PRESETS.map((value) => (
          <Button
            key={value}
            type="button"
            size="sm"
            variant={preset === value && !customOpen ? "secondary" : "ghost"}
            aria-pressed={preset === value && !customOpen}
            className="h-7 px-2.5 text-xs"
            onClick={() => {
              setCustomOpen(false);
              onChange(presetPeriod(value));
            }}
          >
            {t(`keyPage.period.presets.${value}`)}
          </Button>
        ))}
        <Button
          type="button"
          size="sm"
          variant={showCustom ? "secondary" : "ghost"}
          aria-pressed={showCustom}
          aria-expanded={showCustom}
          className="h-7 px-2.5 text-xs"
          onClick={() => {
            setDraft(period);
            setCustomOpen((open) => !open || preset === "custom");
          }}
        >
          {t("keyPage.period.presets.custom")}
        </Button>
      </div>
      {showCustom ? (
        <form onSubmit={apply} className="flex flex-wrap items-end gap-2" aria-label={t("keyPage.period.custom")}>
          <div className="space-y-0.5">
            <Label htmlFor="key-page-from" className="text-[11px] text-muted-foreground">{t("keyPage.period.from")}</Label>
            <Input
              id="key-page-from"
              type="date"
              className="h-7 w-36 text-xs"
              value={draft.from}
              max={draft.to || today}
              onChange={(event) => setDraft({ ...draft, from: event.target.value })}
            />
          </div>
          <div className="space-y-0.5">
            <Label htmlFor="key-page-to" className="text-[11px] text-muted-foreground">{t("keyPage.period.to")}</Label>
            <Input
              id="key-page-to"
              type="date"
              className="h-7 w-36 text-xs"
              value={draft.to}
              min={draft.from || undefined}
              max={today}
              onChange={(event) => setDraft({ ...draft, to: event.target.value })}
            />
          </div>
          <Button type="submit" size="sm" className="h-7 text-xs" disabled={!draftValid}>
            {t("keyPage.period.apply")}
          </Button>
          {!draftValid ? (
            <p className="w-full text-[11px] text-destructive" role="alert">
              {t("keyPage.period.invalid", { days: KEY_PAGE_MAX_DAYS })}
            </p>
          ) : null}
        </form>
      ) : null}
      <p className={cn("text-xs tabular-nums text-muted-foreground", showCustom ? "w-full" : "ml-auto")}>
        {t("keyPage.period.summary", { from: period.from, to: period.to, days: periodDays(period), zone: timezone })}
      </p>
    </div>
  );
}
