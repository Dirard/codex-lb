import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { AccountGroupDialog } from "@/features/account-groups/components/account-group-dialog";
import { useAccountGroups } from "@/features/account-groups/hooks/use-account-groups";
import { getApiKey, updateApiKey } from "@/features/api-keys/api";
import { ApiKeyEditDialog } from "@/features/api-keys/components/api-key-edit-dialog";
import type { ApiKeyUpdateRequest } from "@/features/api-keys/schemas";
import { invalidateEntityViews } from "@/lib/invalidate-entity-views";

export const LazyOauthDialog = lazy(() =>
  import("@/features/accounts/components/oauth-dialog").then((m) => ({ default: m.OauthDialog })),
);

/** The key edit dialog for one key, loaded through GET /api/api-keys/{id} when opened. */
export function KeyLimitsDialog({ keyId, onClose }: { keyId: string; onClose: () => void }) {
  const queryClient = useQueryClient();
  const { t } = useTranslation();
  const keyQuery = useQuery({ queryKey: ["api-keys", "detail", keyId], queryFn: () => getApiKey(keyId) });
  const save = useMutation({
    mutationFn: (payload: ApiKeyUpdateRequest) => updateApiKey(keyId, payload),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.updated"));
      void queryClient.invalidateQueries({ queryKey: ["api-keys"] });
      void invalidateEntityViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.updateFailed")),
  });
  return (
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
  );
}

/** The group dialog with its limit templates, for a grouped key's limits. */
export function GroupLimitsDialog({ groupId, onClose }: { groupId: string; onClose: () => void }) {
  const { groupsQuery, updateMutation } = useAccountGroups();
  const group = groupsQuery.data?.find((entry) => entry.id === groupId) ?? null;
  return (
    <AccountGroupDialog
      open
      busy={updateMutation.isPending || groupsQuery.isPending}
      group={group}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      onSubmit={async (payload) => {
        await updateMutation.mutateAsync({ groupId, payload });
        onClose();
      }}
    />
  );
}
