import { useTranslation } from "react-i18next";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { REQUESTS_TIMEFRAMES, type RequestsTimeframe } from "@/features/dashboard/schemas";

function isRequestsTimeframe(value: string): value is RequestsTimeframe {
  return (REQUESTS_TIMEFRAMES as readonly string[]).includes(value);
}

export type RequestsTimeframeSelectProps = {
  value: RequestsTimeframe;
  onChange: (value: RequestsTimeframe) => void;
};

/** The Requests page's only period control, shared by both views. */
export function RequestsTimeframeSelect({ value, onChange }: RequestsTimeframeSelectProps) {
  const { t } = useTranslation();

  return (
    <Select value={value} onValueChange={(next) => { if (isRequestsTimeframe(next)) onChange(next); }}>
      <SelectTrigger size="sm" className="w-32" aria-label={t("requests.period")}>
        <SelectValue placeholder={t("requests.period")} />
      </SelectTrigger>
      <SelectContent align="end">
        {REQUESTS_TIMEFRAMES.map((timeframe) => (
          <SelectItem key={timeframe} value={timeframe}>
            {t(`requests.timeframes.${timeframe}`)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
