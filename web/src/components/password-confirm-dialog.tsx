import { useState } from "react";
import { useTranslation } from "react-i18next";

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

export type PasswordConfirmDialogProps = {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  busy?: boolean;
  onConfirm: (password: string) => void;
  onOpenChange: (open: boolean) => void;
};

export function PasswordConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  busy = false,
  onConfirm,
  onOpenChange,
}: PasswordConfirmDialogProps) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const close = (next: boolean) => {
    if (!next) setPassword("");
    onOpenChange(next);
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (!password) return;
            onConfirm(password);
            close(false);
          }}
        >
          <Input
            type="password"
            autoComplete="current-password"
            aria-label={t("common.passwordConfirm.label")}
            placeholder={t("common.passwordConfirm.label")}
            value={password}
            disabled={busy}
            onChange={(event) => setPassword(event.target.value)}
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => close(false)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={busy || !password}>
              {confirmLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
