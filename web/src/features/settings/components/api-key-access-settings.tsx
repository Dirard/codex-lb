import { KeySquare } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { ApiKeyAuthToggle } from "@/features/api-keys/components/api-key-auth-toggle";
import { ApiKeyQuotaPrivacyToggle } from "@/features/api-keys/components/api-key-quota-privacy-toggle";

export type ApiKeyAccessSettingsProps = {
  apiKeyAuthEnabled: boolean;
  hideUpstreamQuotaFromApiKeys: boolean;
  disabled?: boolean;
  onApiKeyAuthEnabledChange: (enabled: boolean) => void;
  onHideUpstreamQuotaFromApiKeysChange: (enabled: boolean) => void;
};

/**
 * The installation-wide API-key settings (R41): API-key authentication and
 * the upstream-quota privacy of key reports. Keys and groups themselves are
 * managed on the Keys and Groups pages.
 */
export function ApiKeyAccessSettings({
  apiKeyAuthEnabled,
  hideUpstreamQuotaFromApiKeys,
  disabled = false,
  onApiKeyAuthEnabledChange,
  onHideUpstreamQuotaFromApiKeysChange,
}: ApiKeyAccessSettingsProps) {
  const { t } = useTranslation();
  return (
    <section className="space-y-3 rounded-xl border bg-card p-5">
      <div className="flex items-center gap-2.5">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
          <KeySquare className="h-4 w-4 text-primary" aria-hidden="true" />
        </div>
        <div>
          <h3 className="text-sm font-semibold">{t("settings.apiKeyAccess.title")}</h3>
          <p className="text-xs text-muted-foreground">
            {t("settings.apiKeyAccess.description")}{" "}
            <Link className="font-medium text-primary hover:underline" to="/keys">
              {t("settings.apiKeyAccess.keysLink")}
            </Link>
            {" · "}
            <Link className="font-medium text-primary hover:underline" to="/groups">
              {t("settings.apiKeyAccess.groupsLink")}
            </Link>
          </p>
        </div>
      </div>
      <ApiKeyAuthToggle enabled={apiKeyAuthEnabled} disabled={disabled} onChange={onApiKeyAuthEnabledChange} />
      <ApiKeyQuotaPrivacyToggle enabled={hideUpstreamQuotaFromApiKeys} disabled={disabled} onChange={onHideUpstreamQuotaFromApiKeysChange} />
    </section>
  );
}
