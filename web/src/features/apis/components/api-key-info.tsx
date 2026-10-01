import { useTranslation } from "react-i18next";

import type { ApiKey } from "@/features/api-keys/schemas";
import { ApiKeyLimitDetails } from "./api-key-limit-usage";
import { useDateDisplayFormatStore, type DateDisplayFormat } from "@/hooks/use-date-format";
import { cn } from "@/lib/utils";
import {
	formatCompactNumber,
	formatCurrency,
	formatTimeLong,
} from "@/utils/formatters";

export type ApiKeyInfoProps = {
	apiKey: ApiKey;
	usageSummary?: ApiKey["usageSummary"] | null;
	usageMessage?: string | null;
	allowUsageSummaryFallback?: boolean;
};

function formatExpiry(value: string | null, neverLabel: string, displayFormat: DateDisplayFormat): string {
	if (!value) return neverLabel;
	const parsed = formatTimeLong(value, displayFormat);
	return `${parsed.date} ${parsed.time}`;
}

function isExpired(apiKey: ApiKey): boolean {
	if (!apiKey.expiresAt) return false;
	return new Date(apiKey.expiresAt).getTime() < Date.now();
}

export function ApiKeyInfo({
	apiKey,
	usageSummary,
	usageMessage,
	allowUsageSummaryFallback = true,
}: ApiKeyInfoProps) {
	const { t } = useTranslation();
	const dateDisplayFormat = useDateDisplayFormatStore((state) => state.dateDisplayFormat);
	const expired = isExpired(apiKey);
	const models = apiKey.allowedModels?.join(", ") || t("apiKeys.modelSelect.all");
	const enforcedModel = apiKey.enforcedModel || null;
	const enforcedEffort = apiKey.enforcedReasoningEffort || null;
	const allowedEfforts = apiKey.allowedReasoningEfforts
		?.map((effort) => t(`common.reasoning.${effort}`))
		.join(", ") || null;
	const trafficClass = apiKey.trafficClass === "opportunistic" ? t("common.traffic.opportunistic") : t("common.traffic.foreground");
	const usage = allowUsageSummaryFallback
		? (usageSummary ?? apiKey.usageSummary)
		: (usageSummary ?? null);
	const hasUsage = usage && usage.requestCount > 0;

	return (
		<div className="space-y-4 rounded-lg border bg-muted/30 p-4" data-testid="api-key-info">
			<h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
				{t("apis.keyInfo.title")}
			</h3>
			<dl className="space-y-2 text-xs">
				<div className="flex items-center justify-between gap-2">
					<dt className="text-muted-foreground">{t("apiKeys.table.prefix")}</dt>
					<dd className="font-mono font-medium">{apiKey.keyPrefix}</dd>
				</div>
				<div className="flex items-center justify-between gap-2">
					<dt className="text-muted-foreground">{t("apiKeys.table.models")}</dt>
					<dd className="text-right font-medium">{models}</dd>
				</div>
				<div className="flex items-center justify-between gap-2">
					<dt className="text-muted-foreground">{t("apiKeys.form.trafficClass")}</dt>
					<dd className="font-medium">{trafficClass}</dd>
				</div>
				{enforcedModel ? (
					<div className="flex items-center justify-between gap-2">
						<dt className="text-muted-foreground">{t("apiKeys.form.enforcedModel")}</dt>
						<dd className="font-mono font-medium">{enforcedModel}</dd>
					</div>
				) : null}
				{enforcedEffort ? (
					<div className="flex items-center justify-between gap-2">
						<dt className="text-muted-foreground">{t("apiKeys.form.enforcedReasoning")}</dt>
						<dd className="font-medium">{enforcedEffort}</dd>
					</div>
				) : null}
				{allowedEfforts ? (
					<div className="flex items-start justify-between gap-2">
						<dt className="text-muted-foreground">{t("apiKeys.form.allowedReasoningEfforts")}</dt>
						<dd className="text-right font-medium">{allowedEfforts}</dd>
					</div>
				) : null}
				<div className="flex items-center justify-between gap-2">
					<dt className="text-muted-foreground">{t("apiKeys.table.expiry")}</dt>
					<dd
						className={cn(
							"font-medium",
							expired ? "text-red-600 dark:text-red-400" : "",
						)}
					>
						{expired ? t("common.states.expired") : formatExpiry(apiKey.expiresAt, t("common.time.never"), dateDisplayFormat)}
					</dd>
				</div>
				<div className="flex items-start justify-between gap-2">
					<dt className="text-muted-foreground">{t("apiKeys.table.usage")}</dt>
					<dd className="text-right tabular-nums">
						{hasUsage ? (
							<span>
								<span className="font-medium">
									{t("common.units.tokensShort", { count: formatCompactNumber(usage.totalTokens) })}
								</span>
								<span className="mx-1 text-muted-foreground/40">|</span>
								<span className="font-medium">
									{t("common.units.cachedShort", { count: formatCompactNumber(usage.cachedInputTokens) })}
								</span>
								<span className="mx-1 text-muted-foreground/40">|</span>
								<span className="font-medium">
									{t("common.units.requestsShort", { count: formatCompactNumber(usage.requestCount) })}
								</span>
								<span className="mx-1 text-muted-foreground/40">|</span>
								<span className="font-medium">
									{formatCurrency(usage.totalCostUsd)}
								</span>
							</span>
						) : (
							<span className="text-muted-foreground">
								{usageMessage ?? t("apis.keyInfo.noUsageRecorded")}
							</span>
						)}
					</dd>
				</div>
				<div className="space-y-1.5">
					<div className="flex items-center justify-between gap-2">
						<dt className="text-muted-foreground">{t("apiKeys.form.limits")}</dt>
						<dd className="text-right tabular-nums">
							{apiKey.limits.length > 0 ? (
								<span className="font-medium">
									{t("apis.keyInfo.limitsConfigured", { count: apiKey.limits.length })}
								</span>
							) : (
								<span className="text-muted-foreground">
									{t("apis.keyInfo.noLimitsConfigured")}
								</span>
							)}
						</dd>
					</div>
					<ApiKeyLimitDetails limits={apiKey.limits} />
				</div>
			</dl>
		</div>
	);
}
