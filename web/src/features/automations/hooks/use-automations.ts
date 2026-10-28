import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  createAutomation,
  deleteAutomation,
  runAutomationNow,
  updateAutomation,
} from "@/features/automations/api";
import type {
  AutomationCreateRequest,
  AutomationUpdateRequest,
} from "@/features/automations/schemas";

/**
 * Create, update, run and delete automation jobs. Listing and run history are
 * served by useAutomationListing; every mutation invalidates both.
 */
export function useAutomationMutations() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ["automations"] });
  };

  const createMutation = useMutation({
    mutationFn: (payload: AutomationCreateRequest) => createAutomation(payload),
    onSuccess: async () => {
      toast.success(t("automations.toasts.created"));
      await invalidate();
    },
    onError: (error: Error) => {
      toast.error(error.message || t("automations.toasts.createFailed"));
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ automationId, payload }: { automationId: string; payload: AutomationUpdateRequest }) =>
      updateAutomation(automationId, payload),
    onSuccess: async () => {
      toast.success(t("automations.toasts.updated"));
      await invalidate();
    },
    onError: (error: Error) => {
      toast.error(error.message || t("automations.toasts.updateFailed"));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (automationId: string) => deleteAutomation(automationId),
    onSuccess: async () => {
      toast.success(t("automations.toasts.deleted"));
      await invalidate();
    },
    onError: (error: Error) => {
      toast.error(error.message || t("automations.toasts.deleteFailed"));
    },
  });

  const runNowMutation = useMutation({
    mutationFn: (automationId: string) => runAutomationNow(automationId),
    onSuccess: async () => {
      toast.success(t("automations.toasts.runQueued"));
      await invalidate();
    },
    onError: (error: Error) => {
      toast.error(error.message || t("automations.toasts.runFailed"));
    },
  });

  return {
    createMutation,
    updateMutation,
    deleteMutation,
    runNowMutation,
  };
}
