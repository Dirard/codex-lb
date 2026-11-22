import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { updateAccount } from "@/features/accounts/api";
import {
  createModelSource,
  deleteModelSource,
  listModelSources,
  updateModelSource,
} from "@/features/model-sources/api";
import type {
  ModelSourceCreateRequest,
  ModelSourceUpdateRequest,
} from "@/features/model-sources/schemas";

export function useModelSources() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const { data, error, isFetching, isLoading, isPending, isSuccess, refetch } = useQuery({
    queryKey: ["model-sources", "list"],
    queryFn: listModelSources,
  });
  const modelSourcesQuery = { data, error, isFetching, isLoading, isPending, isSuccess, refetch };

  const createMutation = useMutation({
    mutationFn: (payload: ModelSourceCreateRequest) => createModelSource(payload),
    onSuccess: () => {
      toast.success(t("modelSources.toasts.created"));
      void queryClient.invalidateQueries({ queryKey: ["model-sources", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["api-keys", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["models"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("modelSources.toasts.createFailed"));
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ sourceId, payload }: { sourceId: string; payload: ModelSourceUpdateRequest }) =>
      updateModelSource(sourceId, payload),
    onSuccess: () => {
      toast.success(t("modelSources.toasts.updated"));
      void queryClient.invalidateQueries({ queryKey: ["model-sources", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["api-keys", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["models"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("modelSources.toasts.updateFailed"));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (sourceId: string) => deleteModelSource(sourceId),
    onSuccess: () => {
      toast.success(t("modelSources.toasts.deleted"));
      void queryClient.invalidateQueries({ queryKey: ["model-sources", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["api-keys", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["models"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("modelSources.toasts.deleteFailed"));
    },
  });

  // A source is an account row; its sticky-only option is an account option.
  const stickyMutation = useMutation({
    mutationFn: ({ sourceId, stickyOnly }: { sourceId: string; stickyOnly: boolean }) =>
      updateAccount(sourceId, { stickyOnly }),
    onSuccess: () => {
      toast.success(t("modelSources.toasts.updated"));
      void queryClient.invalidateQueries({ queryKey: ["model-sources", "list"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("modelSources.toasts.updateFailed"));
    },
  });

  return {
    modelSourcesQuery,
    createMutation,
    updateMutation,
    deleteMutation,
    stickyMutation,
  };
}
