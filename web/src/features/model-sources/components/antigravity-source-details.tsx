import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { antigravityAccountState, safeActionLink } from "@/features/model-sources/components/model-source-form";
import type { ModelSource } from "@/features/model-sources/schemas";

/** Email, plan, status and the appeal or verification link of an Antigravity source. */
export function AntigravitySourceDetails({ source }: { source: ModelSource }) {
  const { t } = useTranslation();
  const account = source.providerConfig?.antigravity;
  const state = antigravityAccountState(source);
  const link = safeActionLink(account?.actionUrl);
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <span className="truncate">{t("modelSources.antigravity.listAccount", { email: account?.email ?? source.name })}</span>
      {account?.tier ? <span>{t("modelSources.antigravity.tier", { tier: account.tier })}</span> : null}
      {state === "reauth_required" ? (
        <Badge variant="destructive">{t("modelSources.antigravity.status.reauthRequired")}</Badge>
      ) : state === "suspended" ? (
        <Badge variant="destructive">{t("modelSources.antigravity.status.suspended")}</Badge>
      ) : state === "active" ? (
        <Badge variant="outline">{t("modelSources.antigravity.status.active")}</Badge>
      ) : (
        <Badge variant="secondary">{source.accountStatus}</Badge>
      )}
      {link && state !== "active" ? (
        <a className="font-medium text-primary underline" href={link} target="_blank" rel="noreferrer noopener">
          {state === "suspended" ? t("modelSources.antigravity.appeal") : t("modelSources.antigravity.verify")}
        </a>
      ) : null}
    </div>
  );
}
