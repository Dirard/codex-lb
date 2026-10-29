import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Ellipsis, Link2, Pencil } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/confirm-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { resetAccountGroupUsage } from "@/features/account-groups/api";
import { deleteApiKey, regenerateApiKey, updateApiKey } from "@/features/api-keys/api";
import { ApiKeyCreatedDialog } from "@/features/api-keys/components/api-key-created-dialog";
import { ApiKeyEditLoader } from "@/features/api-keys/components/api-key-edit-loader";
import type { ApiKeyUpdateRequest } from "@/features/api-keys/schemas";
import { invalidateKeyViews, KEY_PAGE_QUERY_KEY } from "@/features/key-page/api";
import type { KeyIdentity } from "@/features/key-page/components/key-page-header";
import { ownerKeyPageLink } from "@/features/key-page/owner-link";
import type { KeyPagePeriod } from "@/features/key-page/period";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { copyToClipboard } from "@/utils/clipboard";

const UNGROUPED = "__ungrouped__";

type Confirmation = "disable" | "regenerate" | "reset" | "delete";

/** Moves the key into a group of the directory, or out of its group. */
function MoveToGroupDialog({
  groupId,
  busy,
  onClose,
  onMove,
}: {
  groupId: string | null;
  busy: boolean;
  onClose: () => void;
  onMove: (groupId: string | null) => void;
}) {
  const { t } = useTranslation();
  const directory = useDirectory();
  const [target, setTarget] = useState(groupId ?? UNGROUPED);
  const groups = directory.data?.groups ?? [];
  const next = target === UNGROUPED ? null : target;
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("keyPage.actions.moveTitle")}</DialogTitle>
          <DialogDescription>{t("keyPage.actions.moveDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="key-page-move-group">{t("keyPage.header.group")}</Label>
          <Select value={target} onValueChange={setTarget} disabled={directory.isPending}>
            <SelectTrigger id="key-page-move-group" className="w-full">
              <SelectValue placeholder={t("keyPage.header.ungrouped")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={UNGROUPED}>{t("keyPage.header.ungrouped")}</SelectItem>
              {groups.map((group) => (
                <SelectItem key={group.id} value={group.id}>{group.name}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="button" disabled={busy || next === groupId} onClick={() => onMove(next)}>
            {t("keyPage.actions.move")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The administrator's key actions through the existing endpoints: Edit,
 * Edit limits (ungrouped) or Open group limits (grouped), Disable or Enable,
 * Copy owner link, Move to group, Regenerate, Reset period (grouped, for the
 * whole group) and Delete. Every mutation refreshes the key page, the key
 * lists, the overview, the directory and the shell status.
 */
export function KeyAdminActions({
  keyId,
  identity,
  period,
}: {
  keyId: string;
  identity: KeyIdentity | null;
  period: KeyPagePeriod;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [moving, setMoving] = useState(false);
  const [confirm, setConfirm] = useState<Confirmation | null>(null);
  const [createdKey, setCreatedKey] = useState<string | null>(null);
  const groupId = identity?.groupId ?? null;
  const groupName = identity?.groupName ?? groupId ?? "";
  const disabled = identity?.status === "disabled";

  const patch = useMutation({
    mutationFn: (payload: ApiKeyUpdateRequest) => updateApiKey(keyId, payload),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.updated"));
      void invalidateKeyViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.updateFailed")),
  });
  const regenerate = useMutation({
    mutationFn: () => regenerateApiKey(keyId),
    onSuccess: (result) => {
      toast.success(t("apiKeys.toasts.regenerated"));
      setCreatedKey(result.key);
      void invalidateKeyViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.regenerateFailed")),
  });
  const resetPeriod = useMutation({
    mutationFn: () => resetAccountGroupUsage(groupId ?? ""),
    onSuccess: () => {
      toast.success(t("accountGroups.toasts.periodReset"));
      void queryClient.invalidateQueries({ queryKey: ["account-groups"] });
      void invalidateKeyViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("accountGroups.toasts.periodResetFailed")),
  });
  const remove = useMutation({
    mutationFn: () => deleteApiKey(keyId),
    onSuccess: () => {
      toast.success(t("apiKeys.toasts.deleted"));
      queryClient.removeQueries({ queryKey: [...KEY_PAGE_QUERY_KEY, "admin", keyId] });
      navigate("/keys");
      void invalidateKeyViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("apiKeys.toasts.deleteFailed")),
  });
  const busy = patch.isPending || regenerate.isPending || resetPeriod.isPending || remove.isPending;

  const copyOwnerLink = async () => {
    const copied = await copyToClipboard(ownerKeyPageLink(period));
    if (copied) toast.success(t("keyPage.actions.ownerLinkCopied"));
    else toast.error(t("components.copyButton.toasts.failed"));
  };

  const confirmation: Record<Confirmation, { title: string; description: string; label: string; run: () => void }> = {
    disable: {
      title: t("overview.actions.disableConfirmTitle", { name: identity?.name ?? keyId }),
      description: t("overview.actions.disableConfirmDescription"),
      label: t("common.actions.disable"),
      run: () => patch.mutate({ isActive: false }),
    },
    regenerate: {
      title: t("keyPage.actions.regenerateTitle"),
      description: t("keyPage.actions.regenerateDescription"),
      label: t("common.actions.regenerate"),
      run: () => regenerate.mutate(),
    },
    reset: {
      title: t("accountGroups.resetDialog.title"),
      description: t("accountGroups.resetDialog.description", { name: groupName }),
      label: t("keyPage.actions.resetPeriod"),
      run: () => resetPeriod.mutate(),
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
    <>
      <Button type="button" size="sm" variant="outline" className="h-8 gap-1.5 text-xs" disabled={busy} onClick={() => setEditing(true)}>
        <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
        {t("common.actions.edit")}
      </Button>
      {groupId ? (
        <Button asChild size="sm" variant="outline" className="h-8 text-xs">
          <Link to={`/settings?group=${encodeURIComponent(groupId)}#account-groups`}>{t("keyPage.actions.openGroupLimits")}</Link>
        </Button>
      ) : (
        <Button type="button" size="sm" variant="outline" className="h-8 text-xs" disabled={busy} onClick={() => setEditing(true)}>
          {t("keyPage.actions.editLimits")}
        </Button>
      )}
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="h-8 text-xs"
        disabled={busy || !identity}
        onClick={() => (disabled ? patch.mutate({ isActive: true }) : setConfirm("disable"))}
      >
        {disabled ? t("common.actions.enable") : t("common.actions.disable")}
      </Button>
      <Button type="button" size="sm" variant="outline" className="h-8 gap-1.5 text-xs" onClick={() => void copyOwnerLink()}>
        <Link2 className="h-3.5 w-3.5" aria-hidden="true" />
        {t("keyPage.actions.copyOwnerLink")}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button type="button" size="icon-sm" variant="ghost" disabled={busy} aria-label={t("keyPage.actions.more")}>
            <Ellipsis className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => setMoving(true)}>{t("keyPage.actions.moveToGroup")}</DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setConfirm("regenerate")}>{t("common.actions.regenerate")}</DropdownMenuItem>
          {groupId ? (
            <DropdownMenuItem onSelect={() => setConfirm("reset")}>{t("keyPage.actions.resetPeriod")}</DropdownMenuItem>
          ) : null}
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setConfirm("delete")}>{t("common.actions.delete")}</DropdownMenuItem>
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
      {editing ? (
        <ApiKeyEditLoader keyId={keyId} onClose={() => setEditing(false)} onSaved={() => void invalidateKeyViews(queryClient)} />
      ) : null}
      {moving ? (
        <MoveToGroupDialog
          groupId={groupId}
          busy={patch.isPending}
          onClose={() => setMoving(false)}
          onMove={(next) => {
            patch.mutate({ groupId: next }, { onSuccess: () => setMoving(false) });
          }}
        />
      ) : null}
      <ApiKeyCreatedDialog
        open={createdKey !== null}
        apiKey={createdKey}
        onOpenChange={(open) => {
          if (!open) setCreatedKey(null);
        }}
      />
    </>
  );
}
