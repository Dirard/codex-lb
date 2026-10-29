import { useMutation, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/confirm-dialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { deleteApiKey, regenerateApiKey, updateApiKey } from "@/features/api-keys/api";
import { ApiKeyCreatedDialog } from "@/features/api-keys/components/api-key-created-dialog";
import { ApiKeyEditLoader } from "@/features/api-keys/components/api-key-edit-loader";
import { invalidateKeyViews } from "@/features/key-page/api";
import type { KeyRow } from "@/features/keys/schemas";

type Confirmation = "disable" | "regenerate" | "delete";

/**
 * The row menu of the keys table: Edit, Disable or Enable, Regenerate and
 * Delete through the existing key endpoints. Disable, Regenerate and Delete
 * ask first; every change refreshes the key lists, the key page, the
 * directory and the overview.
 */
export function KeyRowActions({ row }: { row: KeyRow }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [confirm, setConfirm] = useState<Confirmation | null>(null);
  const [createdKey, setCreatedKey] = useState<string | null>(null);
  const refresh = () => void invalidateKeyViews(queryClient);

  const setActive = useMutation({
    mutationFn: (isActive: boolean) => updateApiKey(row.id, { isActive }),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.updated"));
      refresh();
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.updateFailed")),
  });
  const regenerate = useMutation({
    mutationFn: () => regenerateApiKey(row.id),
    onSuccess: (result) => {
      toast.success(t("apiKeys.toasts.regenerated"));
      setCreatedKey(result.key);
      refresh();
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.regenerateFailed")),
  });
  const remove = useMutation({
    mutationFn: () => deleteApiKey(row.id),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.deleted"));
      refresh();
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.deleteFailed")),
  });
  const busy = setActive.isPending || regenerate.isPending || remove.isPending;
  const disabled = row.status === "disabled";

  const confirmation: Record<Confirmation, { title: string; description: string; label: string; run: () => void }> = {
    disable: {
      title: t("overview.actions.disableConfirmTitle", { name: row.name }),
      description: t("overview.actions.disableConfirmDescription"),
      label: t("common.actions.disable"),
      run: () => setActive.mutate(false),
    },
    regenerate: {
      title: t("keyPage.actions.regenerateTitle"),
      description: t("keyPage.actions.regenerateDescription"),
      label: t("common.actions.regenerate"),
      run: () => regenerate.mutate(),
    },
    delete: {
      title: t("apiKeys.deleteDialog.title"),
      description: t("apiKeys.deleteDialog.description"),
      label: t("common.actions.delete"),
      run: () => remove.mutate(),
    },
  };
  const active = confirm ? confirmation[confirm] : null;

  return (
    // The menu lives inside a clickable row; its clicks must not open the key.
    <div onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="h-7 w-7"
            disabled={busy}
            aria-label={t("keys.table.rowActions", { name: row.name })}
          >
            <MoreHorizontal className="h-3.5 w-3.5" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => setEditing(true)}>{t("common.actions.edit")}</DropdownMenuItem>
          <DropdownMenuItem onSelect={() => (disabled ? setActive.mutate(true) : setConfirm("disable"))}>
            {disabled ? t("common.actions.enable") : t("common.actions.disable")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setConfirm("regenerate")}>{t("common.actions.regenerate")}</DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setConfirm("delete")}>
            {t("common.actions.delete")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={active !== null}
        title={active?.title ?? ""}
        description={active?.description}
        confirmLabel={active?.label}
        confirmDisabled={busy}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
        onConfirm={() => {
          active?.run();
          setConfirm(null);
        }}
      />
      {editing ? <ApiKeyEditLoader keyId={row.id} onClose={() => setEditing(false)} onSaved={refresh} /> : null}
      <ApiKeyCreatedDialog
        open={createdKey !== null}
        apiKey={createdKey}
        onOpenChange={(open) => {
          if (!open) setCreatedKey(null);
        }}
      />
    </div>
  );
}
