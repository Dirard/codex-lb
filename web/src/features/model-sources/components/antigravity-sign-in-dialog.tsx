import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { completeAntigravitySignIn } from "@/features/model-sources/api";
import type { AntigravitySignInStart } from "@/features/model-sources/schemas";
import { getErrorMessageOrNull } from "@/utils/errors";

export type AntigravitySignInDialogProps = {
  /** The started flow: its authorization link and the flow to complete. */
  start: AntigravitySignInStart;
  /** Set when an existing source is being re-authorized. */
  reauthorizing?: boolean;
  /** Starts a new flow after this one was used or expired. */
  onRestart: () => void;
  onClose: () => void;
};

/**
 * Step 1 opens Google's sign-in in a new tab; step 2 takes the code Google's
 * page shows. The code works once, so a failed attempt offers a new sign-in.
 */
export function AntigravitySignInDialog({ start, reauthorizing = false, onRestart, onClose }: AntigravitySignInDialogProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [code, setCode] = useState("");
  const complete = useMutation({
    mutationFn: () => completeAntigravitySignIn(start.flowId, code),
    onSuccess: (source) => {
      toast.success(t("modelSources.antigravity.toasts.signedIn", { email: source.providerConfig?.antigravity?.email ?? source.name }));
      void queryClient.invalidateQueries({ queryKey: ["model-sources", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["api-keys", "list"] });
      void queryClient.invalidateQueries({ queryKey: ["models"] });
      onClose();
    },
  });
  const problem = getErrorMessageOrNull(complete.error);
  const expires = new Date(start.expiresAt).toLocaleTimeString();
  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {reauthorizing ? t("modelSources.antigravity.dialog.reauthTitle") : t("modelSources.antigravity.dialog.title")}
          </DialogTitle>
          <DialogDescription>{t("modelSources.antigravity.dialog.description")}</DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (code.trim()) complete.mutate();
          }}
        >
          <div className="space-y-2">
            <p className="text-sm">{t("modelSources.antigravity.dialog.step1")}</p>
            <a
              className="inline-block text-sm font-medium text-primary underline"
              href={start.authorizationUrl}
              target="_blank"
              rel="noreferrer noopener"
            >
              {t("modelSources.antigravity.dialog.open")}
            </a>
          </div>
          <div className="space-y-2">
            <label htmlFor="antigravity-code" className="text-sm">
              {t("modelSources.antigravity.dialog.step2")}
            </label>
            <Input
              id="antigravity-code"
              value={code}
              onChange={(event) => setCode(event.target.value)}
              placeholder={t("modelSources.antigravity.dialog.codePlaceholder")}
              autoComplete="off"
              spellCheck={false}
            />
            <p className="text-xs text-muted-foreground">{t("modelSources.antigravity.dialog.expires", { time: expires })}</p>
          </div>
          {problem ? <AlertMessage variant="error">{problem}</AlertMessage> : null}
          <DialogFooter>
            {complete.isError ? (
              <Button type="button" variant="outline" onClick={onRestart}>
                {t("modelSources.antigravity.dialog.restart")}
              </Button>
            ) : null}
            <Button type="submit" disabled={!code.trim() || complete.isPending || complete.isError}>
              {reauthorizing ? t("modelSources.antigravity.dialog.reauthSubmit") : t("modelSources.antigravity.dialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
