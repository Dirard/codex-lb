import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cancelVideoLogin, getVideoLoginStatus } from "@/features/video/api";
import type { VideoLoginStart } from "@/features/video/schemas";
import { getErrorMessageOrNull } from "@/utils/errors";

export type VideoLoginDialogProps = {
  login: VideoLoginStart;
  onClose: () => void;
  onSignedIn: (email: string) => void;
};

/** Shows xAI's link and code and polls until the administrator approves the sign-in in another tab. */
export function VideoLoginDialog({ login, onClose, onSignedIn }: VideoLoginDialogProps) {
  const { t } = useTranslation();
  const cancel = useMutation({ mutationFn: () => cancelVideoLogin(login.flowId) });
  const status = useQuery({
    queryKey: ["video", "login", login.flowId],
    queryFn: () => getVideoLoginStatus(login.flowId),
    // The administrator approves in another tab, so polling must not pause here.
    refetchIntervalInBackground: true,
    refetchInterval: (query) => (query.state.data?.status === "pending" || !query.state.data ? Math.max(login.intervalSeconds, 1) * 1000 : false),
    retry: false,
  });
  const signedIn = useRef(onSignedIn);
  useEffect(() => {
    signedIn.current = onSignedIn;
  }, [onSignedIn]);
  const email = status.data?.status === "success" ? (status.data.email ?? "") : null;
  useEffect(() => {
    if (email !== null) signedIn.current(email);
  }, [email]);

  const problem = status.data?.status === "error" ? status.data.message : getErrorMessageOrNull(status.error);
  const close = () => {
    if (email === null) cancel.mutate();
    onClose();
  };
  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : close())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("video.login.title")}</DialogTitle>
          <DialogDescription>{t("video.login.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div>
            <p className="text-xs text-muted-foreground">{t("video.login.code")}</p>
            <p className="font-mono text-2xl font-semibold tracking-wider">{login.userCode}</p>
          </div>
          <a className="inline-block text-sm font-medium text-primary underline" href={login.verificationUrl} target="_blank" rel="noreferrer">
            {t("video.login.open")}
          </a>
          {problem ? <AlertMessage variant="error">{problem}</AlertMessage> : <p className="text-sm text-muted-foreground">{t("video.login.waiting")}</p>}
        </div>
      </DialogContent>
    </Dialog>
  );
}
