import { Pencil, Plus, RotateCcw, Trash2, Users } from "lucide-react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { AccountGroupDialog } from "@/features/account-groups/components/account-group-dialog";
import { useAccountGroups } from "@/features/account-groups/hooks/use-account-groups";
import type { AccountGroup, AccountGroupCreateRequest } from "@/features/account-groups/schemas";
import type { LimitRuleCreate } from "@/features/api-keys/schemas";
import { useDialogState } from "@/hooks/use-dialog-state";
import { formatCompactNumber } from "@/utils/formatters";
import { getErrorMessageOrNull } from "@/utils/errors";

export type AccountGroupsSectionProps = {
  disabled?: boolean;
};

function formatGroupLimits(
  limits: LimitRuleCreate[],
  t: ReturnType<typeof useTranslation>["t"],
): string {
  if (limits.length === 0) {
    return t("apiKeys.table.noLimit");
  }
  return limits
    .map((limit) => {
      const type = t(`apiKeys.limitTypes.${limit.limitType}`, {
        defaultValue: limit.limitType,
      });
      const value =
        limit.limitType === "cost_usd"
          ? `$${(limit.maxValue / 1_000_000).toFixed(2)}`
          : formatCompactNumber(limit.maxValue);
      return `${type} ${value}/${limit.limitWindow}`;
    })
    .join(", ");
}

export function AccountGroupsSection({ disabled = false }: AccountGroupsSectionProps) {
  const { t } = useTranslation();
  const { groupsQuery, createMutation, updateMutation, deleteMutation, resetUsageMutation } = useAccountGroups();
  const createDialog = useDialogState();
  const editDialog = useDialogState<AccountGroup>();
  const deleteDialog = useDialogState<AccountGroup>();
  const resetDialog = useDialogState<AccountGroup>();
  const groups = groupsQuery.data ?? [];
  const busy =
    disabled ||
    groupsQuery.isFetching ||
    createMutation.isPending ||
    updateMutation.isPending ||
    deleteMutation.isPending ||
    resetUsageMutation.isPending;
  const error =
    getErrorMessageOrNull(groupsQuery.error) ||
    getErrorMessageOrNull(createMutation.error) ||
    getErrorMessageOrNull(updateMutation.error) ||
    getErrorMessageOrNull(deleteMutation.error);

  const createGroup = async (payload: AccountGroupCreateRequest) => {
    await createMutation.mutateAsync(payload);
  };

  const updateGroup = async (groupId: string, payload: Parameters<typeof updateMutation.mutateAsync>[0]["payload"]) => {
    await updateMutation.mutateAsync({ groupId, payload });
  };

  return (
    <section className="space-y-4 rounded-xl border bg-card p-5">
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2.5">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
            <Users className="h-4 w-4 text-primary" aria-hidden="true" />
          </div>
          <div>
            <h3 className="text-sm font-semibold">{t("accountGroups.title")}</h3>
            <p className="text-xs text-muted-foreground">{t("accountGroups.description")}</p>
          </div>
        </div>
        <Button
          type="button"
          size="sm"
          className="h-8 gap-1.5 text-xs"
          disabled={busy}
          onClick={() => createDialog.show()}
        >
          <Plus className="h-3.5 w-3.5" />
          {t("accountGroups.actions.addGroup")}
        </Button>
      </div>

      {error ? <AlertMessage variant="error">{error}</AlertMessage> : null}

      <div className="space-y-2" aria-busy={groupsQuery.isLoading}>
        {groupsQuery.isLoading ? (
          <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
            {t("common.loading")}
          </div>
        ) : groups.length > 0 ? (
          groups.map((group) => (
            <div key={group.id} className="rounded-lg border p-3">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{group.name}</span>
                    <Badge variant="secondary">
                      {t("accountGroups.badges.keys", { count: group.keyCount })}
                    </Badge>
                    <Badge variant="secondary">
                      {t("accountGroups.badges.accounts", { count: group.accountIds.length })}
                    </Badge>
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {formatGroupLimits(group.limits, t)}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy || group.keyCount === 0 || group.limits.length === 0}
                    aria-label={t("accountGroups.actions.resetAria", { name: group.name })}
                    onClick={() => {
                      resetUsageMutation.reset();
                      resetDialog.show(group);
                    }}
                  >
                    <RotateCcw className="size-4" aria-hidden="true" />
                    {t("accountGroups.actions.resetPeriod")}
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => editDialog.show(group)}
                  >
                    <Pencil className="size-4" />
                    <span className="sr-only">
                      {t("accountGroups.actions.editAria", { name: group.name })}
                    </span>
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => deleteDialog.show(group)}
                  >
                    <Trash2 className="size-4" />
                    <span className="sr-only">
                      {t("accountGroups.actions.deleteAria", { name: group.name })}
                    </span>
                  </Button>
                </div>
              </div>
            </div>
          ))
        ) : (
          <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
            {t("accountGroups.empty")}
          </div>
        )}
      </div>

      <AccountGroupDialog
        open={createDialog.open}
        busy={createMutation.isPending}
        group={null}
        onOpenChange={createDialog.onOpenChange}
        onSubmit={createGroup}
      />

      <AccountGroupDialog
        open={editDialog.open}
        busy={updateMutation.isPending}
        group={editDialog.data}
        onOpenChange={editDialog.onOpenChange}
        onSubmit={(payload) =>
          editDialog.data ? updateGroup(editDialog.data.id, payload) : Promise.resolve()
        }
      />

      <ConfirmDialog
        open={resetDialog.open}
        title={t("accountGroups.resetDialog.title")}
        description={t("accountGroups.resetDialog.description", { name: resetDialog.data?.name ?? "" })}
        confirmLabel={t("accountGroups.actions.resetPeriod")}
        confirmDisabled={resetUsageMutation.isPending}
        cancelDisabled={resetUsageMutation.isPending}
        keepOpenOnConfirm
        onOpenChange={(open) => {
          if (!resetUsageMutation.isPending) resetDialog.onOpenChange(open);
        }}
        onConfirm={() => {
          if (!resetDialog.data || resetUsageMutation.isPending) return;
          resetUsageMutation.mutate(resetDialog.data.id, { onSuccess: () => resetDialog.hide() });
        }}
      >
        {resetUsageMutation.error ? <AlertMessage variant="error">{getErrorMessageOrNull(resetUsageMutation.error)}</AlertMessage> : null}
      </ConfirmDialog>

      <ConfirmDialog
        open={deleteDialog.open}
        title={t("accountGroups.deleteDialog.title")}
        description={t("accountGroups.deleteDialog.description")}
        confirmLabel={t("common.actions.delete")}
        onOpenChange={deleteDialog.onOpenChange}
        onConfirm={() => {
          if (!deleteDialog.data) {
            return;
          }
          void deleteMutation.mutateAsync(deleteDialog.data.id).finally(() => {
            deleteDialog.hide();
          });
        }}
      />
    </section>
  );
}
