import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/lib/api-client";
import { getSettings, updateSettings } from "@/features/settings/api";
import type { SettingsUpdateRequest } from "@/features/settings/schemas";

export function useSettings() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const { data, error, isFetching, isLoading, isPending, isSuccess, refetch } = useQuery({
    queryKey: ["settings", "detail"],
    queryFn: getSettings,
  });
  const settingsQuery = { data, error, isFetching, isLoading, isPending, isSuccess, refetch };

  const updateSettingsMutation = useMutation({
    mutationFn: (payload: SettingsUpdateRequest) => updateSettings(payload),
    onSuccess: () => {
      toast.success(t("settings.toasts.saved"));
      void queryClient.invalidateQueries({ queryKey: ["settings", "detail"] });
    },
    onError: (error: Error) => {
      toast.error(error.message || t("settings.toasts.saveFailed"));
      if (error instanceof ApiError && error.code === "settings_conflict") {
        // Another writer committed since this form was loaded; refetch so the
        // next save carries the fresh expectedVersion.
        void queryClient.invalidateQueries({ queryKey: ["settings", "detail"] });
      }
    },
  });

  return {
    settingsQuery,
    updateSettingsMutation,
  };
}
