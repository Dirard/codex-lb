import { Suspense, lazy, useState } from "react";
import { Settings } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useLocation } from "react-router-dom";

import { AlertMessage } from "@/components/alert-message";
import { LoadingOverlay } from "@/components/layout/loading-overlay";
import { Button } from "@/components/ui/button";
import { AccountGroupsSection } from "@/features/account-groups/components/account-groups-section";
import { ApiKeysSection } from "@/features/api-keys/components/api-keys-section";
import { useAccounts } from "@/features/accounts/hooks/use-accounts";
import { FirewallSection } from "@/features/firewall/components/firewall-section";
import { ModelSourcesSettings } from "@/features/model-sources/components/model-sources-settings";
import { ModelPricesSettings } from "@/features/model-prices/model-prices-settings";
import { RuntimeUpdateSettings } from "@/features/runtime/runtime-update-settings";
import { buildSettingsUpdateRequest } from "@/features/settings/payload";
import { shouldExpandAdvancedSettings } from "@/features/settings/advanced-settings-deeplink";
import { AdvancedSettingsGroup } from "@/features/settings/components/advanced-settings-group";
import { AppearanceSettings } from "@/features/settings/components/appearance-settings";
import { CodexClientSettings } from "@/features/settings/components/codex-client-settings";
import { DataRetentionSettings } from "@/features/settings/components/data-retention-settings";
import { ImportSettings } from "@/features/settings/components/import-settings";
import { PasswordSettings } from "@/features/settings/components/password-settings";
import { ResetCreditSettings } from "@/features/settings/components/reset-credit-settings";
import { RoutingSettings } from "@/features/settings/components/routing-settings";
import { SessionSettings } from "@/features/settings/components/session-settings";
import { SettingsSkeleton } from "@/features/settings/components/settings-skeleton";
import { StickySessionsSection } from "@/features/sticky-sessions/components/sticky-sessions-section";
import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { useSettings } from "@/features/settings/hooks/use-settings";
import type { SettingsUpdateRequest } from "@/features/settings/schemas";
import { getErrorMessageOrNull } from "@/utils/errors";

const TotpSettings = lazy(() =>
  import("@/features/settings/components/totp-settings").then((m) => ({ default: m.TotpSettings })),
);

const FIREWALL_LAYOUT_QUERY_KEYS = [
  ["accounts", "list"],
  ["model-sources", "list"],
] as const;

export function SettingsPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const expandAdvanced = shouldExpandAdvancedSettings(location.search, location.hash);
  const advancedScrollToId = location.hash.replace(/^#/, "") || undefined;
  const { settingsQuery, updateSettingsMutation } = useSettings();
  const [initialRetryError, setInitialRetryError] = useState<string | null>(null);
  const { accountsQuery } = useAccounts();
  const authMode = useAuthStore((state) => state.authMode);
  const passwordManagementEnabled = useAuthStore((state) => state.passwordManagementEnabled);
  const passwordSessionActive = useAuthStore((state) => state.passwordSessionActive);
  const canWrite = useAuthStore((state) => state.canWrite);

  const settings = settingsQuery.data;
  const busy = updateSettingsMutation.isPending;
  const controlsDisabled = busy || !canWrite;
  const settingsLoadError = getErrorMessageOrNull(
    settingsQuery.error,
    t("settings.toasts.loadFailed"),
  );
  const displayedSettingsLoadError = settingsLoadError || initialRetryError;
  // With no settings loaded the failed-load branch below owns this message, so
  // the page-level alert would otherwise render it a second time.
  const error =
    (settings ? settingsLoadError : null) ||
    getErrorMessageOrNull(updateSettingsMutation.error);

  const handleSave = async (payload: SettingsUpdateRequest) => {
    await updateSettingsMutation.mutateAsync(payload);
  };

  return (
    <div className="animate-fade-in-up space-y-6">
      {/* Page header */}
      <div>
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <Settings className="h-5 w-5 text-primary" />
          {t("settings.page.title")}
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("settings.page.subtitle")}</p>
      </div>

      {settingsQuery.isPending && !settings && initialRetryError === null ? (
        <SettingsSkeleton />
      ) : !settings ? (
        <div className="space-y-3 rounded-xl border bg-card p-4">
          <div role="alert">
            <AlertMessage variant="error">
              {displayedSettingsLoadError || t("settings.toasts.loadFailed")}
            </AlertMessage>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              setInitialRetryError(displayedSettingsLoadError || t("settings.toasts.loadFailed"));
              void settingsQuery.refetch().finally(() => {
                setInitialRetryError(null);
              });
            }}
            disabled={settingsQuery.isFetching || initialRetryError !== null}
          >
            {t("common.actions.retry")}
          </Button>
        </div>
      ) : (
        <>
          {error ? <AlertMessage variant="error">{error}</AlertMessage> : null}
          {!canWrite ? (
            <div className="rounded-lg border border-primary/20 bg-primary/5 px-3 py-2 text-xs font-medium text-foreground">
              {t("settings.page.readOnlyNotice")}
            </div>
          ) : null}

          {authMode === "trusted_header" ? (
            <div className="rounded-lg border border-primary/20 bg-primary/5 px-3 py-2 text-xs font-medium text-foreground">
              {t("settings.page.trustedHeaderNotice")}
            </div>
          ) : null}

          {authMode === "disabled" ? (
            <div className="rounded-lg border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs font-medium text-foreground">
              {t("settings.page.disabledNotice")}
            </div>
          ) : null}

          <div className="space-y-4">
            <AppearanceSettings />
            <RuntimeUpdateSettings canWrite={canWrite} />
            <ImportSettings settings={settings} busy={controlsDisabled} onSave={handleSave} />
            <ResetCreditSettings settings={settings} busy={controlsDisabled} onSave={handleSave} />
            {canWrite ? <PasswordSettings disabled={busy} /> : null}
            {canWrite && passwordManagementEnabled ? (
              <SessionSettings settings={settings} busy={busy} onSave={handleSave} />
            ) : null}
            {canWrite && passwordManagementEnabled && passwordSessionActive ? (
              <Suspense fallback={null}>
                <TotpSettings settings={settings} disabled={busy} onSave={handleSave} />
              </Suspense>
            ) : null}

            <ApiKeysSection
              apiKeyAuthEnabled={settings.apiKeyAuthEnabled}
              hideUpstreamQuotaFromApiKeys={settings.hideUpstreamQuotaFromApiKeys}
              disabled={controlsDisabled}
              onApiKeyAuthEnabledChange={(enabled) =>
                void handleSave(buildSettingsUpdateRequest(settings, { apiKeyAuthEnabled: enabled }))
              }
              onHideUpstreamQuotaFromApiKeysChange={(enabled) =>
                void handleSave(buildSettingsUpdateRequest(settings, { hideUpstreamQuotaFromApiKeys: enabled }))
              }
            />

            <AccountGroupsSection disabled={controlsDisabled} />
            <ModelPricesSettings disabled={controlsDisabled} />

            <AdvancedSettingsGroup
              key={expandAdvanced ? `open:${advancedScrollToId ?? ""}` : "closed"}
              defaultOpen={expandAdvanced}
              scrollToId={advancedScrollToId}
              waitForQueryKeys={FIREWALL_LAYOUT_QUERY_KEYS}
            >
              <CodexClientSettings
                key={settings.codexClientVersion}
                settings={settings}
                busy={controlsDisabled}
                onSave={handleSave}
              />
              <RoutingSettings
                key={[
                  settings.openaiCacheAffinityMaxAgeSeconds,
                  settings.warmupModel,
                  settings.limitWarmupModel,
                  settings.limitWarmupPrompt,
                  settings.limitWarmupExhaustedThresholdPercent,
                  settings.limitWarmupIdleThresholdPercent,
                  settings.limitWarmupCooldownSeconds,
                  settings.limitWarmupStaggeredIdleEnabled,
                   settings.proxyAccountResponseCreateLimit,
                   settings.proxyAccountResponseCreateLimitOverride,
                   settings.proxyAccountStreamLimit,
                   settings.proxyAccountStreamLimitOverride,
                   settings.proxyAccountStreamRecoveryReserve,
                   settings.proxyAccountStreamRecoveryReserveOverride,
                   settings.proxyApiKeyFairShareCongestionThresholdPct,
                   settings.proxyApiKeyFairShareCongestionThresholdPctOverride,
                ].join(":")}
                settings={settings}
                accounts={accountsQuery.data ?? []}
                accountsLoading={accountsQuery.isLoading}
                busy={controlsDisabled}
                onSave={handleSave}
              />
              <ModelSourcesSettings disabled={controlsDisabled} />
              <FirewallSection disabled={controlsDisabled} />
              <StickySessionsSection disabled={controlsDisabled} />
              <DataRetentionSettings
                key={[
                  settings.requestLogRetentionOverrideDays,
                  settings.usageHistoryRetentionOverrideDays,
                  settings.requestLogRetentionDays,
                  settings.usageHistoryRetentionDays,
                ].join(":")}
                settings={settings}
                busy={controlsDisabled}
                onSave={handleSave}
              />
            </AdvancedSettingsGroup>
          </div>

          <LoadingOverlay visible={!!settings && busy} label={t("settings.page.savingLabel")} />
        </>
      )}
    </div>
  );
}
