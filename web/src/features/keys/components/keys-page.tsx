import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, KeyRound } from "lucide-react";
import { lazy, Suspense, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { AlertMessage } from "@/components/alert-message";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { createApiKey } from "@/features/api-keys/api";
import { ApiKeyCreatedDialog } from "@/features/api-keys/components/api-key-created-dialog";
import type { ApiKeyCreateRequest } from "@/features/api-keys/schemas";
import { invalidateKeyViews } from "@/features/key-page/api";
import { DEFAULT_KEYS_PARAMS, hasKeyFilters, KEYS_PAGE_SIZE } from "@/features/keys/api";
import { BulkActionsBar } from "@/features/keys/components/bulk-actions-bar";
import { KeysFilters } from "@/features/keys/components/keys-filters";
import { KeysTable } from "@/features/keys/components/keys-table";
import { useKeySelection, useKeysList } from "@/features/keys/hooks/use-keys-list";
import type { KeySort } from "@/features/keys/schemas";
import { getErrorMessageOrNull } from "@/utils/errors";

const ApiKeyCreateDialog = lazy(() =>
  import("@/features/api-keys/components/api-key-create-dialog").then((m) => ({ default: m.ApiKeyCreateDialog })),
);

/**
 * The Keys page (dashboard redesign use case 6): search and filters in the
 * URL, one server-sorted page of 50 keys polled every 60 s while visible, a
 * selection that survives polls and pages, bulk actions and pagination.
 */
export function KeysPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { params, update, query, timezone } = useKeysList();
  const selection = useKeySelection();
  const [creating, setCreating] = useState(false);
  const [createdKey, setCreatedKey] = useState<string | null>(null);

  const page = query.data;
  const rows = useMemo(() => page?.rows ?? [], [page]);
  const total = page?.total ?? null;
  const order = params.order ?? (params.sort === "name" ? "asc" : "desc");
  const pages = total === null ? 1 : Math.max(1, Math.ceil(total / KEYS_PAGE_SIZE));
  const current = Math.floor(params.offset / KEYS_PAGE_SIZE) + 1;
  const filtered = hasKeyFilters(params);
  const error = getErrorMessageOrNull(query.error);

  const create = useMutation({
    mutationFn: (payload: ApiKeyCreateRequest) => createApiKey(payload),
    onSuccess: (created) => {
      toast.success(t("apiKeys.toasts.created"));
      setCreating(false);
      setCreatedKey(created.key);
      void invalidateKeyViews(queryClient);
    },
    onError: (failure: Error) => toast.error(failure.message || t("apiKeys.toasts.createFailed")),
  });

  const sortBy = (sort: KeySort) => {
    if (sort === params.sort) update({ order: order === "asc" ? "desc" : "asc" });
    else update({ sort, order: null });
  };

  return (
    // Room below the table for the floating bulk bar while keys are selected.
    <div className={selection.ids.size > 0 ? "animate-fade-in-up space-y-4 pb-20" : "animate-fade-in-up space-y-4"}>
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("keys.page.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("keys.page.subtitle")}</p>
      </div>

      <KeysFilters params={params} total={total} onChange={update} onCreate={() => setCreating(true)} />

      {error ? (
        <AlertMessage variant="error">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span>{page ? t("keys.page.refreshFailed", { error }) : error}</span>
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" disabled={query.isFetching} onClick={() => void query.refetch()}>
              {t("common.actions.retry")}
            </Button>
          </div>
        </AlertMessage>
      ) : null}

      <BulkActionsBar selection={selection} total={total ?? 0} params={params} timezone={timezone} rows={rows} />

      {page && page.total === 0 ? (
        filtered ? (
          <EmptyState
            icon={KeyRound}
            title={t("keys.page.noMatches")}
            action={
              <Button type="button" size="sm" variant="outline" onClick={() => update({ ...DEFAULT_KEYS_PARAMS, sort: params.sort, order: params.order })}>
                {t("keys.filters.reset")}
              </Button>
            }
          />
        ) : (
          <EmptyState
            icon={KeyRound}
            title={t("keys.page.emptyTitle")}
            description={t("keys.page.emptyDescription")}
            action={
              <Button type="button" size="sm" onClick={() => setCreating(true)}>
                {t("keys.filters.create")}
              </Button>
            }
          />
        )
      ) : page || query.isPending ? (
        <KeysTable
          rows={rows}
          loading={query.isPending}
          sort={params.sort}
          order={order}
          selected={selection.ids}
          onSelect={selection.set}
          onSort={sortBy}
        />
      ) : null}

      {page && page.total > 0 ? (
        <nav className="flex items-center justify-end gap-2 text-xs text-muted-foreground" aria-label={t("keys.page.pagination")}>
          <span className="tabular-nums" data-testid="keys-page-of">
            {t("keys.page.pageOf", { page: current, pages })}
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-7 gap-1 text-xs"
            disabled={params.offset === 0}
            onClick={() => update({ offset: Math.max(0, params.offset - KEYS_PAGE_SIZE) })}
          >
            <ChevronLeft className="h-3.5 w-3.5" aria-hidden="true" />
            {t("keys.page.previous")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-7 gap-1 text-xs"
            disabled={params.offset + KEYS_PAGE_SIZE >= page.total}
            onClick={() => update({ offset: params.offset + KEYS_PAGE_SIZE })}
          >
            {t("keys.page.next")}
            <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </nav>
      ) : null}

      <Suspense fallback={null}>
        <ApiKeyCreateDialog
          open={creating}
          busy={create.isPending}
          onOpenChange={setCreating}
          onSubmit={async (payload) => {
            await create.mutateAsync(payload);
          }}
        />
      </Suspense>
      <ApiKeyCreatedDialog
        open={createdKey !== null}
        apiKey={createdKey}
        onOpenChange={(open) => {
          if (!open) setCreatedKey(null);
        }}
      />
    </div>
  );
}
