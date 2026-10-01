import { useId, useState } from "react";
import type { FormEvent } from "react";
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
import { Label } from "@/components/ui/label";

export type ImportDialogProps = {
  open: boolean;
  busy: boolean;
  error: string | null;
  onOpenChange: (open: boolean) => void;
  onImport: (file: File) => Promise<void>;
};

const MAX_AUTH_JSON_BYTES = 1024 * 1024;

// Mount only while open so closing the dialog discards its credential draft.
function ImportForm({
  busy,
  error,
  onOpenChange,
  onImport,
}: Omit<ImportDialogProps, "open">) {
  const { t } = useTranslation();
  const id = useId();
  const [mode, setMode] = useState<"file" | "paste">("file");
  const [file, setFile] = useState<File | null>(null);
  const [text, setText] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);
  const hasInput = mode === "file" ? file !== null && file.size > 0 : text.trim().length > 0;

  const changeMode = (next: "file" | "paste") => {
    setMode(next);
    setFile(null);
    setText("");
    setLocalError(null);
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || !hasInput) {
      return;
    }
    const authFile = mode === "paste" ? new File([text], "auth.json", { type: "application/json" }) : file;
    if (!authFile) return;
    if (authFile.size > MAX_AUTH_JSON_BYTES) {
      setLocalError(t("accounts.importDialog.tooLarge"));
      return;
    }
    setLocalError(null);
    try {
      await onImport(authFile);
      setFile(null);
      setText("");
      onOpenChange(false);
    } catch {
      // The account mutation supplies safe server feedback through the error prop.
    }
  };

  const visibleError = localError ?? error;
  return (
    <form className="space-y-4" onSubmit={handleSubmit} aria-busy={busy}>
      <fieldset disabled={busy} className="space-y-2">
        <legend className="text-sm font-medium">{t("accounts.importDialog.method")}</legend>
        <div className="flex gap-4">
          {(["file", "paste"] as const).map((value) => (
            <label key={value} className="flex items-center gap-2 text-sm">
              <input type="radio" name={`${id}-method`} value={value} checked={mode === value} onChange={() => changeMode(value)} />
              {t(value === "file" ? "accounts.importDialog.chooseFile" : "accounts.importDialog.pasteJson")}
            </label>
          ))}
        </div>
      </fieldset>

      <div className="space-y-2">
        {mode === "file" ? (
          <>
            <Label htmlFor={`${id}-file`}>{t("accounts.importDialog.fileLabel")}</Label>
            <Input
              id={`${id}-file`}
              type="file"
              accept="application/json,.json"
              disabled={busy}
              onChange={(event) => { setFile(event.target.files?.[0] ?? null); setLocalError(null); }}
            />
          </>
        ) : (
          <>
            <Label htmlFor={`${id}-json`}>{t("accounts.importDialog.jsonLabel")}</Label>
            <textarea
              id={`${id}-json`}
              value={text}
              onChange={(event) => { setText(event.target.value); setLocalError(null); }}
              disabled={busy}
              rows={8}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              aria-describedby={`${id}-hint`}
              className="border-input placeholder:text-muted-foreground focus-visible:ring-ring/50 w-full rounded-md border bg-transparent p-3 font-mono text-sm outline-none focus-visible:ring-2 disabled:opacity-50"
            />
            <p id={`${id}-hint`} className="text-muted-foreground text-xs">{t("accounts.importDialog.pasteHint")}</p>
          </>
        )}
      </div>

      {visibleError ? (
        <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-2 py-1 text-xs text-destructive">
          {visibleError}
        </p>
      ) : null}

      <DialogFooter>
        <Button type="submit" disabled={busy || !hasInput}>
          {t("common.actions.import")}
        </Button>
      </DialogFooter>
    </form>
  );
}

export function ImportDialog({ open, ...props }: ImportDialogProps) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={props.onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("accounts.importDialog.title")}</DialogTitle>
          <DialogDescription>{t("accounts.importDialog.description")}</DialogDescription>
        </DialogHeader>

        {open ? <ImportForm {...props} /> : null}
      </DialogContent>
    </Dialog>
  );
}
