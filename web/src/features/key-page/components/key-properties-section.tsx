import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { AccountLabel } from "@/features/key-page/components/key-admin-sections";
import { dateTimeLabel } from "@/features/key-page/key-page-format";
import { CurrentBadge, SectionCard } from "@/features/key-page/components/section-card";
import type { KeyPageKey, KeyPageMode } from "@/features/key-page/schemas";
import type { Directory } from "@/features/search/api";

function Property({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-start justify-between gap-3 border-b py-1.5 last:border-0">
      <dt className="shrink-0 text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right text-xs font-medium">{children}</dd>
    </div>
  );
}

/**
 * The key's current configuration. The administrator sees the assigned
 * accounts by label; the owner sees only the counts (R26). Without an
 * assignment scope the key may use every permitted account or source.
 */
export function KeyPropertiesSection({
  mode,
  details,
  directory,
}: {
  mode: KeyPageMode;
  details: KeyPageKey;
  directory: Directory | undefined;
}) {
  const { t } = useTranslation();
  const models = details.allowedModels?.length ? details.allowedModels.join(", ") : t("apiKeys.modelSelect.all");
  const efforts = details.allowedReasoningEfforts?.length
    ? details.allowedReasoningEfforts.map((effort) => t(`common.reasoning.${effort}`, { defaultValue: effort })).join(", ")
    : t("apiKeys.reasoningEfforts.all");
  const sections = details.usageSections.length
    ? details.usageSections.map((section) => t(`apiKeys.usageSections.${section}`, { defaultValue: section })).join(", ")
    : t("common.options.none");
  const accounts = details.assignedAccounts ?? [];
  return (
    <SectionCard id="key-page-properties" title={t("keyPage.properties.title")} badge={<CurrentBadge />}>
      <dl className="grid gap-x-8 px-4 py-2 md:grid-cols-2">
        <div>
          <Property label={t("apiKeys.form.allowedModels")}>
            <span className="break-words font-mono">{models}</span>
          </Property>
          <Property label={t("apiKeys.form.enforcedModel")}>
            {details.enforcedModel ? <span className="font-mono">{details.enforcedModel}</span> : t("common.options.none")}
          </Property>
          <Property label={t("apiKeys.form.enforcedReasoning")}>
            {details.enforcedReasoningEffort
              ? t(`common.reasoning.${details.enforcedReasoningEffort}`, { defaultValue: details.enforcedReasoningEffort })
              : t("common.options.none")}
          </Property>
          <Property label={t("apiKeys.form.allowedReasoningEfforts")}>{efforts}</Property>
          <Property label={t("apiKeys.form.trafficClass")}>
            {t(`common.traffic.${details.trafficClass}`, { defaultValue: details.trafficClass })}
          </Property>
        </div>
        <div>
          <Property label={t("apiKeys.form.enforcedServiceTier")}>
            {details.serviceTier ? t(`common.serviceTier.${details.serviceTier}`, { defaultValue: details.serviceTier }) : t("common.options.none")}
          </Property>
          <Property label={t("apiKeys.form.usageSections")}>{sections}</Property>
          <Property label={t("keyPage.header.expires")}>
            {details.expiresAt ? dateTimeLabel(details.expiresAt) : t("keyPage.header.neverExpires")}
          </Property>
          <Property label={t("keyPage.properties.accounts")}>
            {!details.accountAssignmentScopeEnabled ? (
              t("keyPage.properties.allAccounts")
            ) : mode === "admin" && accounts.length > 0 ? (
              <ul className="flex flex-col items-end gap-0.5" aria-label={t("keyPage.properties.accounts")}>
                {accounts.map((account) => (
                  <li key={account.id} className="max-w-64">
                    <AccountLabel accountId={account.id} label={account.label} directory={directory} />
                  </li>
                ))}
              </ul>
            ) : (
              t("keyPage.properties.accountCount", { count: details.assignedAccountCount })
            )}
          </Property>
          <Property label={t("keyPage.properties.sources")}>
            {!details.sourceAssignmentScopeEnabled
              ? t("keyPage.properties.allSources")
              : t("keyPage.properties.sourceCount", { count: details.assignedSourceCount })}
          </Property>
        </div>
      </dl>
    </SectionCard>
  );
}
