import { useMutation, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { AlertMessage } from "@/components/alert-message";
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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { LimitRulesEditor } from "@/features/api-keys/components/limit-rules-editor";
import { normalizeLimitRules } from "@/features/api-keys/components/limit-rules-utils";
import type { LimitRuleCreate } from "@/features/api-keys/schemas";
import { invalidateKeyViews } from "@/features/key-page/api";
import { applyKeysBulk, fetchKeyIds, type KeyBulkRequest, type KeysListParams } from "@/features/keys/api";
import type { KeySelection } from "@/features/keys/hooks/use-keys-list";
import type { KeyBulkResult, KeyRow } from "@/features/keys/schemas";
import { useDirectory } from "@/features/search/hooks/use-directory";

const NO_GROUP = "__none__";
const FAILURES_SHOWN = 10;

type Pending = "disable" | "enable" | "move" | "limits";

/** The reason of a per-key failure of R36. */
function failureReason(t: (key: string) => string, code: string | undefined): string {
  switch (code) {
    case "not_found":
    case "key_in_group":
    case "invalid_group":
    case "not_applied":
    case "conflict":
      return t(`keys.bulk.failures.${code}`);
  }
  return t("keys.bulk.failures.internal_error");
}

function MoveDialog({
  count,
  busy,
  onClose,
  onMove,
}: {
  count: number;
  busy: boolean;
  onClose: () => void;
  onMove: (groupId: string | null) => void;
}) {
  const { t } = useTranslation();
  const directory = useDirectory();
  const [target, setTarget] = useState<string | null>(null);
  const groups = directory.data?.groups ?? [];
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("keys.bulk.moveTitle", { count })}</DialogTitle>
          <DialogDescription>{t("keys.bulk.moveDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="keys-bulk-move-group">{t("keys.table.group")}</Label>
          <Select value={target ?? undefined} onValueChange={setTarget} disabled={directory.isPending}>
            <SelectTrigger id="keys-bulk-move-group" className="w-full">
              <SelectValue placeholder={t("keys.bulk.chooseGroup")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NO_GROUP}>{t("keys.bulk.noGroup")}</SelectItem>
              {groups.map((group) => (
                <SelectItem key={group.id} value={group.id}>
                  {group.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="button" disabled={busy || target === null} onClick={() => onMove(target === NO_GROUP ? null : target)}>
            {t("keys.bulk.moveConfirm", { count })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function LimitsDialog({
  count,
  busy,
  onClose,
  onApply,
}: {
  count: number;
  busy: boolean;
  onClose: () => void;
  onApply: (limits: LimitRuleCreate[]) => void;
}) {
  const { t } = useTranslation();
  const [rules, setRules] = useState<LimitRuleCreate[]>([]);
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("keys.bulk.limitsTitle", { count })}</DialogTitle>
          <DialogDescription>{t("keys.bulk.limitsDescription")}</DialogDescription>
        </DialogHeader>
        <LimitRulesEditor rules={rules} onChange={setRules} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            type="button"
            disabled={busy}
            onClick={() =>
              onApply(normalizeLimitRules(rules).map((rule) => ({ ...rule, modelFilter: rule.modelFilter ?? null })))
            }
          >
            {t("keys.bulk.limitsConfirm", { count })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The bulk bar of the keys page (use case 6.1): Disable, Enable, Move to
 * group, Set limits for ungrouped keys and Clear for the selected keys, and
 * "Select all n matching". Every action is confirmed with its name and the
 * number of keys; failed keys stay selected and are listed with their
 * reason; the keys list, the directory and the overview are refreshed (R39).
 */
export function BulkActionsBar({
  selection,
  total,
  params,
  timezone,
  rows,
}: {
  selection: KeySelection;
  total: number;
  params: KeysListParams;
  timezone: string;
  rows: KeyRow[];
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const directory = useDirectory();
  // Failed keys may sit on other pages: the directory names every key.
  const names = useMemo(() => {
    const known = new Map<string, string>();
    for (const key of directory.data?.keys ?? []) known.set(key.id, key.name);
    for (const row of rows) known.set(row.id, row.name);
    return known;
  }, [directory.data, rows]);
  const [pending, setPending] = useState<Pending | null>(null);
  const [failures, setFailures] = useState<KeyBulkResult[]>([]);
  const count = selection.ids.size;

  const apply = useMutation({
    mutationFn: (body: KeyBulkRequest) => applyKeysBulk(body),
    onSuccess: ({ results }) => {
      const failed = results.filter((result) => !result.ok);
      const changed = results.length - failed.length;
      if (changed > 0) toast.success(t("keys.bulk.done", { count: changed }));
      if (failed.length > 0) toast.error(t("keys.bulk.failed", { count: failed.length }));
      setFailures(failed);
      selection.replace(failed.map((result) => result.id));
      setPending(null);
      void invalidateKeyViews(queryClient);
    },
    onError: (error: Error) => toast.error(error.message || t("keys.bulk.requestFailed")),
  });
  const selectAll = useMutation({
    mutationFn: () => fetchKeyIds(params, timezone),
    onSuccess: ({ ids, total: matching }) => {
      selection.set(ids, true);
      if (matching > ids.length) toast.info(t("keys.bulk.selectAllCapped", { count: ids.length, total: matching }));
    },
    onError: (error: Error) => toast.error(error.message || t("keys.bulk.requestFailed")),
  });

  if (count === 0 && failures.length === 0) return null;
  const ids = [...selection.ids];
  const busy = apply.isPending || selectAll.isPending;
  return (
    <div className="space-y-2" data-testid="keys-bulk-bar">
      {count > 0 ? (
        <div className="flex flex-wrap items-center gap-2 rounded-xl border bg-muted/40 px-3 py-2 text-xs">
          <span className="font-medium tabular-nums" data-testid="keys-selected-count">
            {t("keys.bulk.selected", { count })}
          </span>
          {total > count ? (
            <Button type="button" size="sm" variant="link" className="h-7 px-1 text-xs" disabled={busy} onClick={() => selectAll.mutate()}>
              {t("keys.bulk.selectAll", { count: total })}
            </Button>
          ) : null}
          <div className="ml-auto flex flex-wrap items-center gap-1.5">
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" disabled={busy} onClick={() => setPending("disable")}>
              {t("common.actions.disable")}
            </Button>
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" disabled={busy} onClick={() => setPending("enable")}>
              {t("common.actions.enable")}
            </Button>
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" disabled={busy} onClick={() => setPending("move")}>
              {t("keys.bulk.move")}
            </Button>
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" disabled={busy} onClick={() => setPending("limits")}>
              {t("keys.bulk.limits")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 gap-1 text-xs"
              onClick={() => {
                selection.clear();
                setFailures([]);
              }}
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
              {t("keys.bulk.clear")}
            </Button>
          </div>
        </div>
      ) : null}
      {failures.length > 0 ? (
        <AlertMessage variant="error">
          <div className="flex items-start justify-between gap-2">
            <div className="space-y-1">
              <p className="font-medium">{t("keys.bulk.failedTitle", { count: failures.length })}</p>
              <ul className="space-y-0.5 text-xs" data-testid="keys-bulk-failures">
                {failures.slice(0, FAILURES_SHOWN).map((failure) => (
                  <li key={failure.id}>
                    <span className="font-medium">{names.get(failure.id) ?? failure.id}</span> — {failureReason(t, failure.code)}
                  </li>
                ))}
                {failures.length > FAILURES_SHOWN ? (
                  <li>{t("keys.bulk.moreFailures", { count: failures.length - FAILURES_SHOWN })}</li>
                ) : null}
              </ul>
            </div>
            <Button type="button" size="icon" variant="ghost" className="h-6 w-6" aria-label={t("keys.bulk.dismiss")} onClick={() => setFailures([])}>
              <X className="h-3.5 w-3.5" />
            </Button>
          </div>
        </AlertMessage>
      ) : null}
      <ConfirmDialog
        open={pending === "disable" || pending === "enable"}
        title={pending === "enable" ? t("keys.bulk.enableTitle", { count }) : t("keys.bulk.disableTitle", { count })}
        description={pending === "enable" ? t("keys.bulk.enableDescription") : t("keys.bulk.disableDescription")}
        confirmLabel={pending === "enable" ? t("keys.bulk.enableConfirm", { count }) : t("keys.bulk.disableConfirm", { count })}
        confirmDisabled={busy}
        keepOpenOnConfirm
        onOpenChange={(open) => {
          if (!open) setPending(null);
        }}
        onConfirm={() => {
          if (pending === "enable" || pending === "disable") apply.mutate({ ids, action: pending });
        }}
      />
      {pending === "move" ? (
        <MoveDialog
          count={count}
          busy={busy}
          onClose={() => setPending(null)}
          onMove={(groupId) => apply.mutate({ ids, action: "move_group", groupId })}
        />
      ) : null}
      {pending === "limits" ? (
        <LimitsDialog
          count={count}
          busy={busy}
          onClose={() => setPending(null)}
          onApply={(limits) => apply.mutate({ ids, action: "set_limits", limits })}
        />
      ) : null}
    </div>
  );
}
