import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { useShellStatus } from "@/components/layout/use-shell-status";

// Keyless proxy access is an explicit operator choice; keep it visible on
// every dashboard page while API-key authentication is off. The flag comes
// from the shell status, so the warning adds no request of its own.
export function ApiKeyAuthWarning() {
  const { t } = useTranslation();
  const { data } = useShellStatus();
  if (!data || data.apiKeyAuthEnabled) {
    return null;
  }
  return (
    <div role="alert" className="mb-4">
      <AlertMessage variant="warning">{t("apiKeys.authDisabledWarning")}</AlertMessage>
    </div>
  );
}
