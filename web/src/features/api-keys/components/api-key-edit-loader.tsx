import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { getApiKey, updateApiKey } from "@/features/api-keys/api";
import type { ApiKeyUpdateRequest } from "@/features/api-keys/schemas";

const ApiKeyEditDialog = lazy(() =>
  import("@/features/api-keys/components/api-key-edit-dialog").then((m) => ({ default: m.ApiKeyEditDialog })),
);

/**
 * The edit dialog of one key, loaded through GET /api/api-keys/{id} when
 * opened from a page that lists keys without their full settings; onSaved
 * refreshes the views that show the key.
 */
export function ApiKeyEditLoader({
  keyId,
  onClose,
  onSaved,
}: {
  keyId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const keyQuery = useQuery({ queryKey: ["api-keys", "detail", keyId], queryFn: () => getApiKey(keyId) });
  const save = useMutation({
    mutationFn: (payload: ApiKeyUpdateRequest) => updateApiKey(keyId, payload),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.updated"));
      void queryClient.invalidateQueries({ queryKey: ["api-keys", "detail", keyId] });
      onSaved();
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.updateFailed")),
  });
  return (
    <Suspense fallback={null}>
      <ApiKeyEditDialog
        open
        busy={save.isPending || keyQuery.isPending}
        apiKey={keyQuery.data ?? null}
        onOpenChange={(open) => {
          if (!open) onClose();
        }}
        onSubmit={async (payload) => {
          await save.mutateAsync(payload);
          onClose();
        }}
      />
    </Suspense>
  );
}
