import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownToLine } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api-client";
import {
  applyRuntimeUpdate,
  checkRuntimeUpdates,
  getRuntimeUpdateStatus,
  rollbackRuntimeUpdate,
} from "@/features/runtime/api";
import { getErrorMessageOrNull } from "@/utils/errors";

const UPDATE_QUERY_KEY = ["runtime", "updates"] as const;
const TERMINAL_PHASES = new Set(["idle", "succeeded", "failed"]);

type UpdateAction = { kind: "check" } | { kind: "apply" | "rollback"; version: string };

function isBusy(phase: string | undefined): boolean {
  return !!phase && !TERMINAL_PHASES.has(phase);
}

function isRestartInterruption(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}

export function RuntimeUpdateSettings({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [observingOperation, setObservingOperation] = useState(false);
  const [confirmation, setConfirmation] = useState<Extract<UpdateAction, { version: string }> | null>(null);
  const statusQuery = useQuery({
    queryKey: UPDATE_QUERY_KEY,
    queryFn: async () => {
      const status = await getRuntimeUpdateStatus();
      if (!isBusy(status.phase)) setObservingOperation(false);
      return status;
    },
    retry: false,
    refetchInterval: (query) => {
      if (query.state.error instanceof ApiError && query.state.error.status === 401) return false;
      return observingOperation || isBusy(query.state.data?.phase) ? 2_000 : 60_000;
    },
    refetchIntervalInBackground: false,
  });
  const actionMutation = useMutation({
    mutationFn: (action: UpdateAction) => {
      setObservingOperation(true);
      switch (action.kind) {
        case "check": return checkRuntimeUpdates();
        case "apply": return applyRuntimeUpdate(action.version);
        case "rollback": return rollbackRuntimeUpdate(action.version);
      }
    },
    onSuccess: (status) => {
      queryClient.setQueryData(UPDATE_QUERY_KEY, status);
      setObservingOperation(isBusy(status.phase));
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 401) {
        setObservingOperation(false);
        return;
      }
      setObservingOperation(true);
      void queryClient.invalidateQueries({ queryKey: UPDATE_QUERY_KEY });
    },
  });

  const status = statusQuery.data;
  const currentVersion = status?.currentVersion;
  const latestVersion = status?.latestVersion;
  const checkedAt = status?.checkedAt;
  useEffect(() => {
    if (currentVersion !== undefined) void queryClient.invalidateQueries({ queryKey: ["runtime", "version"] });
  }, [queryClient, checkedAt, currentVersion, latestVersion]);
  const busy = isBusy(status?.phase) || actionMutation.isPending;
  const disabled = !canWrite || !status?.supported || busy;
  const reconnecting = isRestartInterruption(statusQuery.error) && (busy || observingOperation);
  const loadError = statusQuery.isError && !reconnecting
    ? getErrorMessageOrNull(statusQuery.error, t("settings.runtime.loadFailed"))
    : null;
  const actionError = getErrorMessageOrNull(actionMutation.error);
  const phaseLabel = status ? t(`settings.runtime.phases.${status.phase}`, { defaultValue: status.phase }) : "";

  return (
    <section className="rounded-xl border bg-card p-5" aria-labelledby="runtime-updates-title">
      <div className="flex items-start gap-2.5">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
          <ArrowDownToLine className="h-4 w-4 text-primary" aria-hidden="true" />
        </div>
        <div>
          <h3 id="runtime-updates-title" className="text-sm font-semibold">{t("settings.runtime.title")}</h3>
          <p className="text-xs text-muted-foreground">{t("settings.runtime.description")}</p>
        </div>
      </div>

      {status ? (
        <div className="mt-4 space-y-2 text-sm">
          <p>{t("settings.runtime.currentVersion", { version: status.currentVersion })}</p>
          {status.latestVersion ? (
            <p>{t("settings.runtime.latestVersion", { version: status.latestVersion })}</p>
          ) : (
            <p className="text-muted-foreground">{t("settings.runtime.latestUnknown")}</p>
          )}
          {status.previousVersion ? (
            <p>{t("settings.runtime.previousVersion", { version: status.previousVersion })}</p>
          ) : null}
          {status.releaseUrl ? (
            <a href={status.releaseUrl} target="_blank" rel="noreferrer" className="inline-block text-primary underline-offset-4 hover:underline">
              {t("settings.runtime.releaseNotes")}
            </a>
          ) : null}
          <p role="status" aria-live="polite">
            {t("settings.runtime.phase", { phase: phaseLabel })}
          </p>
          {status.targetVersion ? <p>{t("settings.runtime.targetVersion", { version: status.targetVersion })}</p> : null}
          {!status.supported ? (
            <AlertMessage variant="warning">{status.unavailableReason || t("settings.runtime.unavailable")}</AlertMessage>
          ) : null}
          {status.lastError ? <AlertMessage variant="error">{status.lastError}</AlertMessage> : null}
        </div>
      ) : statusQuery.isPending ? (
        <p className="mt-4 text-sm text-muted-foreground">{t("settings.runtime.loading")}</p>
      ) : null}

      {reconnecting ? <p className="mt-3 text-sm text-muted-foreground" role="status">{t("settings.runtime.reconnecting")}</p> : null}
      {loadError ? <div className="mt-3" role="alert"><AlertMessage variant="error">{loadError}</AlertMessage></div> : null}
      {actionError ? <div className="mt-3" role="alert"><AlertMessage variant="error">{actionError}</AlertMessage></div> : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <Button type="button" size="sm" variant="outline" disabled={disabled} onClick={() => actionMutation.mutate({ kind: "check" })}>
          {t("settings.runtime.check")}
        </Button>
        <Button
          type="button" size="sm" disabled={disabled || !status?.updateAvailable || !status.latestVersion}
          onClick={() => status?.latestVersion && setConfirmation({ kind: "apply", version: status.latestVersion })}
        >
          {status?.latestVersion && status.updateAvailable
            ? t("settings.runtime.updateTo", { version: status.latestVersion })
            : t("settings.runtime.update")}
        </Button>
        <Button
          type="button" size="sm" variant="outline"
          disabled={disabled || !status?.canRollback || !status.previousVersion}
          onClick={() => status?.previousVersion && setConfirmation({ kind: "rollback", version: status.previousVersion })}
        >
          {status?.previousVersion
            ? t("settings.runtime.rollbackTo", { version: status.previousVersion })
            : t("settings.runtime.rollback")}
        </Button>
        {loadError ? (
          <Button type="button" size="sm" variant="outline" onClick={() => void statusQuery.refetch()}>
            {t("common.actions.retry")}
          </Button>
        ) : null}
      </div>

      <ConfirmDialog
        open={confirmation !== null}
        onOpenChange={(open) => { if (!open) setConfirmation(null); }}
        title={confirmation?.kind === "rollback"
          ? t("settings.runtime.confirmRollback", { version: confirmation.version })
          : t("settings.runtime.confirmUpdate", { version: confirmation?.version ?? "" })}
        description={t("settings.runtime.restartWarning")}
        confirmLabel={confirmation?.kind === "rollback" ? t("settings.runtime.rollback") : t("settings.runtime.update")}
        confirmDisabled={disabled}
        onConfirm={() => {
          if (!confirmation) return;
          actionMutation.mutate(confirmation);
          setConfirmation(null);
        }}
      >
        {confirmation?.kind === "rollback" ? (
          <p className="text-sm text-muted-foreground">{t("settings.runtime.rollbackDataWarning")}</p>
        ) : null}
      </ConfirmDialog>
    </section>
  );
}
