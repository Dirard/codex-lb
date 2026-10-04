import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  createAccountGroup,
  deleteAccountGroup,
  listAccountGroups,
  resetAccountGroupUsage,
  updateAccountGroups,
  updateAccountGroup,
} from "@/features/account-groups/api";
import type {
  AccountGroupCreateRequest,
  AccountGroupUpdateRequest,
} from "@/features/account-groups/schemas";

function invalidateGroupQueries(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: ["account-groups", "list"] });
  void queryClient.invalidateQueries({ queryKey: ["api-keys", "list"] });
}

export function useAccountGroups() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const { data, error, isFetching, isLoading, isPending, isSuccess, refetch } = useQuery({
    queryKey: ["account-groups", "list"],
    queryFn: listAccountGroups,
  });
  const groupsQuery = { data, error, isFetching, isLoading, isPending, isSuccess, refetch };

  const createMutation = useMutation({
    mutationFn: (payload: AccountGroupCreateRequest) => createAccountGroup(payload),
    onSuccess: () => {
      toast.success(t("accountGroups.toasts.created"));
      invalidateGroupQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accountGroups.toasts.createFailed"));
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ groupId, payload }: { groupId: string; payload: AccountGroupUpdateRequest }) =>
      updateAccountGroup(groupId, payload),
    onSuccess: () => {
      toast.success(t("accountGroups.toasts.updated"));
      invalidateGroupQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accountGroups.toasts.updateFailed"));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (groupId: string) => deleteAccountGroup(groupId),
    onSuccess: () => {
      toast.success(t("accountGroups.toasts.deleted"));
      invalidateGroupQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accountGroups.toasts.deleteFailed"));
    },
  });

  const resetUsageMutation = useMutation({
    mutationFn: resetAccountGroupUsage,
    onSuccess: () => {
      toast.success(t("accountGroups.toasts.periodReset"));
      invalidateGroupQueries(queryClient);
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accountGroups.toasts.periodResetFailed"));
    },
  });

  return {
    groupsQuery,
    createMutation,
    updateMutation,
    deleteMutation,
    resetUsageMutation,
  };
}

export function useUpdateAccountGroups() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const mutation = useMutation({
    mutationFn: ({ accountId, groupIds }: { accountId: string; groupIds: string[] }) =>
      updateAccountGroups(accountId, { groupIds }),
    onSuccess: async () => {
      toast.success(t("accountGroups.toasts.membershipsUpdated", { defaultValue: "Account groups updated" }));
      await queryClient.invalidateQueries({ queryKey: ["account-groups", "list"] });
      await queryClient.invalidateQueries({ queryKey: ["api-keys", "list"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("accountGroups.toasts.membershipsUpdateFailed", { defaultValue: "Could not update account groups" }));
    },
  });

  return { accountGroupsMutation: mutation };
}
