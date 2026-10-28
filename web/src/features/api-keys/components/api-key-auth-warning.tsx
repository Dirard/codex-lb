import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { getSettings } from "@/features/settings/api";

// Keyless proxy access is an explicit operator choice; keep it visible on
// every dashboard page while API-key authentication is off.
export function ApiKeyAuthWarning() {
  const { t } = useTranslation();
  const { data } = useQuery({ queryKey: ["settings", "detail"], queryFn: getSettings });
  if (!data || data.apiKeyAuthEnabled) {
    return null;
  }
  return (
    <div role="alert" className="mb-4">
      <AlertMessage variant="warning">{t("apiKeys.authDisabledWarning")}</AlertMessage>
    </div>
  );
}
