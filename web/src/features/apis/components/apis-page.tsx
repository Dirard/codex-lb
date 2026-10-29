import { lazy, Suspense, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { AlertMessage } from "@/components/alert-message";
import { LoadingOverlay } from "@/components/layout/loading-overlay";
import { Button } from "@/components/ui/button";
import type { ApiKeyCreateRequest } from "@/features/api-keys/schemas";
import { ApiKeyCreatedDialog } from "@/features/api-keys/components/api-key-created-dialog";
import { ApiKeysOverview } from "@/features/api-keys/components/api-keys-overview";
import { ApiList } from "@/features/apis/components/api-list";
import { ApisSkeleton } from "@/features/apis/components/apis-skeleton";
import { useApiKeys } from "@/features/apis/hooks/use-apis";
import { useDialogState } from "@/hooks/use-dialog-state";
import { getErrorMessageOrNull } from "@/utils/errors";

const ApiKeyCreateDialog = lazy(() =>
	import("@/features/api-keys/components/api-key-create-dialog").then((m) => ({
		default: m.ApiKeyCreateDialog,
	})),
);

/**
 * The key list until the Keys page replaces it: the overview cards, the
 * searchable list and key creation. Selecting a key opens its key page.
 */
export function ApisPage() {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { apiKeysQuery, createMutation } = useApiKeys();

	const createDialog = useDialogState();
	const createdDialog = useDialogState<string>();

	const apiKeys = useMemo(() => apiKeysQuery.data ?? [], [apiKeysQuery.data]);

	const mutationError = getErrorMessageOrNull(createMutation.error);
	const listError = getErrorMessageOrNull(apiKeysQuery.error);
	const pageError = mutationError || (apiKeysQuery.data ? listError : null);

	const handleCreate = async (payload: ApiKeyCreateRequest) => {
		const created = await createMutation.mutateAsync(payload);
		createdDialog.show(created.key);
	};

	return (
		<div className="animate-fade-in-up space-y-6">
			<div>
				<h1 className="text-2xl font-semibold tracking-tight">{t("apis.page.title")}</h1>
				<p className="mt-1 text-sm text-muted-foreground">
					{t("apis.page.subtitle")}
				</p>
			</div>

			{pageError ? (
				<AlertMessage variant="error">{pageError}</AlertMessage>
			) : null}

			{apiKeysQuery.isPending && !apiKeysQuery.data ? (
				<ApisSkeleton />
			) : !apiKeysQuery.data ? (
				<div className="space-y-3 rounded-xl border bg-card p-4">
					<AlertMessage variant="error">
						{listError ?? t("apiKeys.toasts.loadFailed")}
					</AlertMessage>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => {
							void apiKeysQuery.refetch();
						}}
						disabled={apiKeysQuery.isFetching}
					>
						{t("common.actions.retry")}
					</Button>
				</div>
			) : (
				<div className="grid gap-4 lg:grid-cols-[22rem_minmax(0,1fr)]">
					<div className="rounded-xl border bg-card p-4">
						<ApiList
							apiKeys={apiKeys}
							selectedKeyId={null}
							onSelect={(keyId) => navigate(`/keys/${encodeURIComponent(keyId)}`)}
							onOpenCreate={() => createDialog.show()}
						/>
					</div>
					<ApiKeysOverview apiKeys={apiKeys} />
				</div>
			)}

			<Suspense fallback={null}>
				<ApiKeyCreateDialog
					open={createDialog.open}
					busy={createMutation.isPending}
					onOpenChange={createDialog.onOpenChange}
					onSubmit={handleCreate}
				/>
			</Suspense>

			<ApiKeyCreatedDialog
				open={createdDialog.open}
				apiKey={createdDialog.data}
				onOpenChange={createdDialog.onOpenChange}
			/>

			<LoadingOverlay
				visible={!!apiKeysQuery.data && createMutation.isPending}
				label={t("apiKeys.page.updating")}
			/>
		</div>
	);
}
