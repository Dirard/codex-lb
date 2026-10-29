import { RotateCcw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { AlertMessage } from "@/components/alert-message";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useAccountGroupMutations } from "@/features/account-groups/hooks/use-account-groups";
import type { AccountGroup } from "@/features/account-groups/schemas";
import { LimitRulesEditor } from "@/features/api-keys/components/limit-rules-editor";
import { normalizeLimitRules } from "@/features/api-keys/components/limit-rules-utils";
import type { LimitRuleCreate } from "@/features/api-keys/schemas";
import { GroupMembersSelect } from "@/features/groups/components/group-members-select";
import type { GroupAccount, GroupSummaryRow } from "@/features/groups/schemas";
import { HealthBar } from "@/features/overview/components/health-bar";
import { agoLabel, percentLabel, usdLabel } from "@/features/overview/overview-format";
import { getErrorMessageOrNull } from "@/utils/errors";

type Draft = { name: string; accountIds: string[]; limits: LimitRuleCreate[] };

function draftOf(group: { name: string; accountIds: string[]; limits: LimitRuleCreate[] } | null): Draft {
  return {
    name: group?.name ?? "",
    accountIds: [...(group?.accountIds ?? [])],
    limits: (group?.limits ?? []).map((rule) => ({ ...rule, modelFilter: rule.modelFilter ?? null })),
  };
}

/** A canonical form of a draft for change detection. */
function canonical(draft: Draft): string {
  return JSON.stringify({ name: draft.name.trim(), accountIds: [...draft.accountIds].sort(), limits: normalizeLimitRules(draft.limits) });
}

export type GroupDetailSheetProps = {
  /** The group's current summary row, or null to create a group. */
  row: GroupSummaryRow | null;
  accounts: GroupAccount[];
  onClose: () => void;
  onCreated: (groupId: string) => void;
};

/**
 * One group's sheet (use case 7): name, members, limit templates, top keys by
 * 7-day spend, Reset period and Delete. The draft is created once per opened
 * group and survives the summary polls, which only refresh the read-only
 * parts; Save sends one atomic `PUT /api/account-groups/{id}`.
 */
export function GroupDetailSheet({ row, accounts, onClose, onCreated }: GroupDetailSheetProps) {
  const { t } = useTranslation();
  const { createMutation, updateMutation, deleteMutation, resetUsageMutation } = useAccountGroupMutations();
  const [baseline, setBaseline] = useState<Draft>(() => draftOf(row));
  const [draft, setDraft] = useState<Draft>(baseline);
  const [editorKey, setEditorKey] = useState(0);
  const [confirm, setConfirm] = useState<"reset" | "delete" | null>(null);
  const creating = row === null;
  const groupId = row?.id ?? null;
  const dirty = canonical(draft) !== canonical(baseline);
  const saving = createMutation.isPending || updateMutation.isPending;
  const busy = saving || deleteMutation.isPending || resetUsageMutation.isPending;
  const nameMissing = draft.name.trim() === "";
  const saveError = getErrorMessageOrNull(createMutation.error) || getErrorMessageOrNull(updateMutation.error);

  const save = async () => {
    const payload = { name: draft.name.trim(), accountIds: draft.accountIds, limits: normalizeLimitRules(draft.limits) };
    try {
      if (creating) {
        const created = await createMutation.mutateAsync(payload);
        onCreated(created.id);
        return;
      }
      const saved: AccountGroup = await updateMutation.mutateAsync({ groupId: groupId as string, payload });
      const next = draftOf(saved);
      setBaseline(next);
      setDraft(next);
      setEditorKey((key) => key + 1);
    } catch {
      // The mutation shows the error; the draft stays editable.
    }
  };

  return (
    <Sheet open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl" data-testid="group-detail-sheet">
        <SheetHeader>
          <SheetTitle>{creating ? t("groups.sheet.createTitle") : row.name}</SheetTitle>
          <SheetDescription>{creating ? t("groups.sheet.createDescription") : t("groups.sheet.description")}</SheetDescription>
        </SheetHeader>

        <div className="space-y-5 px-4">
          {!creating ? (
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-xs">
              <HealthBar counts={row.accounts} />
              <span>
                {t("groups.sheet.remaining", { primary: percentLabel(row.primary.remainingPercent), weekly: percentLabel(row.secondary.remainingPercent) })}
              </span>
              <span className="tabular-nums">
                {t("groups.sheet.spend", { today: usdLabel(row.spendTodayUsd), week: usdLabel(row.spend7dUsd), month: usdLabel(row.spend30dUsd) })}
              </span>
            </div>
          ) : null}

          <div className="space-y-1">
            <Label htmlFor="group-name">{t("accountGroups.fields.name")}</Label>
            <Input
              id="group-name"
              value={draft.name}
              maxLength={128}
              autoComplete="off"
              onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
            />
          </div>

          <div className="space-y-1">
            <p className="text-sm font-medium">{t("groups.sheet.members")}</p>
            <GroupMembersSelect
              accounts={accounts}
              value={draft.accountIds}
              disabled={busy}
              onChange={(accountIds) => setDraft((current) => ({ ...current, accountIds }))}
            />
          </div>

          <div className="space-y-1">
            <LimitRulesEditor key={editorKey} rules={draft.limits} onChange={(limits) => setDraft((current) => ({ ...current, limits }))} />
            <p className="text-xs text-muted-foreground">{t("groups.sheet.limitsHint")}</p>
          </div>

          {saveError ? <AlertMessage variant="error">{saveError}</AlertMessage> : null}

          {!creating ? (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <p className="text-sm font-medium">{t("groups.sheet.keys", { count: row.keys.total })}</p>
                <Link className="text-xs font-medium text-primary hover:underline" to={`/keys?group=${encodeURIComponent(groupId as string)}`}>
                  {t("groups.sheet.allKeys")}
                </Link>
              </div>
              {row.topKeys7d.length === 0 ? (
                <p className="text-xs text-muted-foreground">{t("groups.sheet.noKeys")}</p>
              ) : (
                <table className="w-full text-xs">
                  <thead>
                    <tr className="border-b text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                      <th className="py-1 font-medium">{t("groups.columns.name")}</th>
                      <th className="py-1 text-right font-medium">{t("groups.columns.spendToday")}</th>
                      <th className="py-1 text-right font-medium">{t("groups.columns.spend7d")}</th>
                      <th className="py-1 text-right font-medium">{t("groups.columns.spend30d")}</th>
                      <th className="py-1 text-right font-medium">{t("groups.columns.limitFill")}</th>
                      <th className="py-1 text-right font-medium">{t("groups.sheet.lastUsed")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {row.topKeys7d.map((key) => (
                      <tr key={key.id} className="border-b last:border-0">
                        <td className="py-1">
                          <Link className="font-medium hover:underline" to={`/keys/${encodeURIComponent(key.id)}`}>
                            {key.name}
                          </Link>
                          {key.status !== "active" ? (
                            <Badge variant="secondary" className="ml-1.5 text-[10px]">
                              {t(`groups.sheet.keyStatus.${key.status}`, { defaultValue: key.status })}
                            </Badge>
                          ) : null}
                        </td>
                        <td className="py-1 text-right tabular-nums">{usdLabel(key.spendTodayUsd)}</td>
                        <td className="py-1 text-right tabular-nums">{usdLabel(key.spend7dUsd)}</td>
                        <td className="py-1 text-right tabular-nums">{usdLabel(key.spend30dUsd)}</td>
                        <td className="py-1 text-right tabular-nums">{percentLabel(key.maxLimitPercent)}</td>
                        <td className="py-1 text-right text-muted-foreground">{agoLabel(key.lastUsedAt) ?? "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          ) : null}
        </div>

        <SheetFooter className="flex-row flex-wrap items-center justify-between gap-2 border-t">
          {!creating ? (
            <div className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy || row.keys.total === 0 || row.limits.length === 0}
                onClick={() => {
                  resetUsageMutation.reset();
                  setConfirm("reset");
                }}
              >
                <RotateCcw className="size-4" aria-hidden="true" />
                {t("accountGroups.actions.resetPeriod")}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => {
                  deleteMutation.reset();
                  setConfirm("delete");
                }}
              >
                <Trash2 className="size-4" aria-hidden="true" />
                {t("common.actions.delete")}
              </Button>
            </div>
          ) : (
            <span />
          )}
          <div className="flex items-center gap-2">
            {dirty && !creating ? <span className="text-xs text-amber-600">{t("groups.sheet.unsaved")}</span> : null}
            <Button type="button" size="sm" disabled={busy || nameMissing || (!creating && !dirty)} onClick={() => void save()}>
              {creating ? t("common.actions.create") : t("common.actions.save")}
            </Button>
          </div>
        </SheetFooter>
      </SheetContent>

      {!creating ? (
        <>
          <ConfirmDialog
            open={confirm === "reset"}
            title={t("accountGroups.resetDialog.title")}
            description={t("accountGroups.resetDialog.description", { name: row.name })}
            confirmLabel={t("accountGroups.actions.resetPeriod")}
            confirmDisabled={resetUsageMutation.isPending}
            cancelDisabled={resetUsageMutation.isPending}
            keepOpenOnConfirm
            onOpenChange={(open) => {
              if (!open && !resetUsageMutation.isPending) setConfirm(null);
            }}
            onConfirm={() => {
              if (resetUsageMutation.isPending) return;
              resetUsageMutation.mutate(groupId as string, { onSuccess: () => setConfirm(null) });
            }}
          >
            {resetUsageMutation.error ? <AlertMessage variant="error">{getErrorMessageOrNull(resetUsageMutation.error)}</AlertMessage> : null}
          </ConfirmDialog>
          <ConfirmDialog
            open={confirm === "delete"}
            title={t("accountGroups.deleteDialog.title")}
            description={t("accountGroups.deleteDialog.description")}
            confirmLabel={t("common.actions.delete")}
            confirmDisabled={deleteMutation.isPending}
            cancelDisabled={deleteMutation.isPending}
            keepOpenOnConfirm
            onOpenChange={(open) => {
              if (!open && !deleteMutation.isPending) setConfirm(null);
            }}
            onConfirm={() => {
              if (deleteMutation.isPending) return;
              deleteMutation.mutate(groupId as string, {
                onSuccess: () => {
                  setConfirm(null);
                  onClose();
                },
              });
            }}
          >
            {deleteMutation.error ? <AlertMessage variant="error">{getErrorMessageOrNull(deleteMutation.error)}</AlertMessage> : null}
          </ConfirmDialog>
        </>
      ) : null}
    </Sheet>
  );
}
