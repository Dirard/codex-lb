import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Suspense, useCallback, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/confirm-dialog";
import { useAccountMutations } from "@/features/accounts/hooks/use-accounts";
import { useOauth } from "@/features/accounts/hooks/use-oauth";
import { updateApiKey } from "@/features/api-keys/api";
import type { ApiKeyUpdateRequest } from "@/features/api-keys/schemas";
import { GroupLimitsDialog, KeyLimitsDialog, LazyOauthDialog } from "@/features/overview/components/overview-dialogs";
import type { Alert } from "@/features/overview/schemas";
import { invalidateEntityViews } from "@/lib/invalidate-entity-views";

/** 30 days, the expiry extension of the overview (R17). */
const EXTENSION_MS = 30 * 24 * 60 * 60 * 1000;

/** Every action the overview performs or navigates to. */
export type OverviewAction =
  | { kind: "reauth"; target: string }
  | { kind: "resume"; target: string }
  | { kind: "open_account"; target: string }
  | { kind: "open_group"; target: string }
  | { kind: "open_accounts"; target: string }
  | { kind: "open_requests"; target: string }
  | { kind: "open_list"; target: string }
  | { kind: "open_key"; target: string }
  | { kind: "open_key_errors"; target: string }
  | { kind: "edit_limits"; target: string }
  | { kind: "open_group_limits"; target: string }
  | { kind: "extend_expiry"; target: string; name?: string; expiresAt?: string | null }
  | { kind: "disable_key"; target: string; name?: string };

/** The new expiry: 30 days after the later of now and the current expiry. */
export function extendedExpiry(expiresAt: string | null | undefined, now = Date.now()): string {
  const current = expiresAt ? new Date(expiresAt).getTime() : Number.NaN;
  const base = Number.isFinite(current) ? Math.max(now, current) : now;
  return new Date(base + EXTENSION_MS).toISOString();
}

/** The overview action an alert's action names; extend carries the expiry. */
export function alertToAction(alert: Alert): OverviewAction {
  const { kind, target } = alert.action;
  if (kind === "extend_expiry") {
    return { kind, target, name: alert.subject.name, expiresAt: alert.at };
  }
  return { kind, target };
}

export function keyErrorsRoute(keyId: string): string {
  return `/requests?api_key_id=${encodeURIComponent(keyId)}&status=error&timeframe=1h`;
}

/**
 * The overview's actions: navigation, inline confirmations for Extend and
 * Disable through PATCH /api/api-keys/{id}, the key and group limit dialogs
 * and the OAuth re-login. Each mutation refetches the overview, the
 * directory and the shell status. Dialogs mount only while used, so the page
 * issues no request beyond its poll until an action runs.
 */
export function useOverviewActions(): { perform: (action: OverviewAction) => void; dialogs: ReactNode } {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { resumeMutation } = useAccountMutations();
  const oauth = useOauth();
  const [oauthAccountId, setOauthAccountId] = useState<string | null>(null);
  const [limitsKeyId, setLimitsKeyId] = useState<string | null>(null);
  const [limitsGroupId, setLimitsGroupId] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<Extract<OverviewAction, { kind: "extend_expiry" | "disable_key" }> | null>(null);

  const patchKey = useMutation({
    mutationFn: ({ keyId, payload }: { keyId: string; payload: ApiKeyUpdateRequest }) => updateApiKey(keyId, payload),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.updated"));
      void queryClient.invalidateQueries({ queryKey: ["api-keys"] });
      void invalidateEntityViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.updateFailed")),
  });

  const perform = useCallback(
    (action: OverviewAction) => {
      switch (action.kind) {
        case "reauth":
          setOauthAccountId(action.target);
          return;
        case "resume":
          void resumeMutation.mutateAsync(action.target).then(() => invalidateEntityViews(queryClient));
          return;
        case "open_account":
          navigate(`/accounts?selected=${encodeURIComponent(action.target)}`);
          return;
        case "open_group":
          navigate(`/groups?selected=${encodeURIComponent(action.target)}`);
          return;
        case "open_key":
          navigate(`/keys?selected=${encodeURIComponent(action.target)}`);
          return;
        case "open_key_errors":
          navigate(keyErrorsRoute(action.target));
          return;
        case "open_accounts":
        case "open_requests":
        case "open_list":
          navigate(action.target || "/accounts");
          return;
        case "edit_limits":
          setLimitsKeyId(action.target);
          return;
        case "open_group_limits":
          setLimitsGroupId(action.target);
          return;
        case "extend_expiry":
        case "disable_key":
          setConfirm(action);
          return;
      }
    },
    [navigate, queryClient, resumeMutation],
  );

  const confirmTitle =
    confirm?.kind === "extend_expiry"
      ? t("overview.actions.extendConfirmTitle", { name: confirm.name ?? confirm.target })
      : t("overview.actions.disableConfirmTitle", { name: confirm?.name ?? confirm?.target ?? "" });
  const confirmDescription =
    confirm?.kind === "extend_expiry"
      ? t("overview.actions.extendConfirmDescription", {
          date: new Date(extendedExpiry(confirm.expiresAt)).toLocaleString(),
        })
      : t("overview.actions.disableConfirmDescription");

  const dialogs = (
    <>
      <ConfirmDialog
        open={confirm !== null}
        title={confirmTitle}
        description={confirmDescription}
        confirmLabel={confirm?.kind === "extend_expiry" ? t("overview.actions.extend") : t("common.actions.disable")}
        confirmDisabled={patchKey.isPending}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
        onConfirm={() => {
          if (!confirm) return;
          const payload: ApiKeyUpdateRequest =
            confirm.kind === "extend_expiry" ? { expiresAt: extendedExpiry(confirm.expiresAt) } : { isActive: false };
          void patchKey.mutateAsync({ keyId: confirm.target, payload }).finally(() => setConfirm(null));
        }}
      />
      {limitsKeyId ? <KeyLimitsDialog keyId={limitsKeyId} onClose={() => setLimitsKeyId(null)} /> : null}
      {limitsGroupId ? <GroupLimitsDialog groupId={limitsGroupId} onClose={() => setLimitsGroupId(null)} /> : null}
      {oauthAccountId ? (
        <Suspense fallback={null}>
          <LazyOauthDialog
            open
            state={oauth.state}
            onOpenChange={(open) => {
              if (!open) {
                setOauthAccountId(null);
                oauth.reset();
              }
            }}
            onStart={async (method) => {
              await oauth.start(method, oauthAccountId);
            }}
            onComplete={async () => {
              await invalidateEntityViews(queryClient);
            }}
            onManualCallback={async (callbackUrl) => {
              await oauth.manualCallback(callbackUrl);
            }}
            onReset={oauth.reset}
          />
        </Suspense>
      ) : null}
    </>
  );

  return { perform, dialogs };
}
