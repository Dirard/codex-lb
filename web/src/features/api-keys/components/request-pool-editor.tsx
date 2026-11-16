import { useTranslation } from "react-i18next";

import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";

import { parseRequestPool, type RequestPoolDraft } from "./request-pool-utils";

export function RequestPoolEditor({
  idPrefix,
  value,
  onChange,
}: {
  idPrefix: string;
  value: RequestPoolDraft;
  onChange: (value: RequestPoolDraft) => void;
}) {
  const { t } = useTranslation();
  const invalid = value.enabled && parseRequestPool(value) === undefined;
  return (
    <div className="space-y-2 rounded-md border p-2">
      <div className="flex items-center justify-between gap-2">
        <label htmlFor={`${idPrefix}-request-pool`} className="text-sm font-medium">
          {t("apiKeys.requestPool.title")}
        </label>
        <Switch
          id={`${idPrefix}-request-pool`}
          checked={value.enabled}
          onCheckedChange={(enabled) => onChange({ ...value, enabled })}
        />
      </div>
      <p className="text-xs text-muted-foreground">{t("apiKeys.requestPool.hint")}</p>
      {value.enabled ? (
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1">
            <label htmlFor={`${idPrefix}-request-pool-rpm`} className="text-xs text-muted-foreground">
              {t("apiKeys.requestPool.requestsPerMinute")}
            </label>
            <Input
              id={`${idPrefix}-request-pool-rpm`}
              inputMode="numeric"
              value={value.requestsPerMinute}
              onChange={(event) => onChange({ ...value, requestsPerMinute: event.target.value })}
              placeholder="120"
              autoComplete="off"
              aria-invalid={invalid}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor={`${idPrefix}-request-pool-wait`} className="text-xs text-muted-foreground">
              {t("apiKeys.requestPool.maxWait")}
            </label>
            <Input
              id={`${idPrefix}-request-pool-wait`}
              inputMode="numeric"
              value={value.maxWaitSeconds}
              onChange={(event) => onChange({ ...value, maxWaitSeconds: event.target.value })}
              autoComplete="off"
              aria-invalid={invalid}
            />
          </div>
        </div>
      ) : null}
      {invalid ? (
        <p role="alert" className="text-xs text-destructive">
          {t("apiKeys.requestPool.invalid")}
        </p>
      ) : null}
    </div>
  );
}
