import { useState } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { useSetAccountGroups } from "@/features/accounts/hooks/use-accounts";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { getErrorMessageOrNull } from "@/utils/errors";

export type AccountGroupsEditorProps = {
  accountId: string;
  /** The account's current group ids, from the account list. */
  currentGroupIds: string[];
  busy: boolean;
};

/**
 * Chooses the account's groups among every group of the directory and saves
 * them with one `PUT /api/accounts/{id}/groups`.
 */
export function AccountGroupsEditor({ accountId, currentGroupIds, busy }: AccountGroupsEditorProps) {
  const { t } = useTranslation();
  const directoryQuery = useDirectory();
  const groupsMutation = useSetAccountGroups();
  const [draftGroupIds, setDraftGroupIds] = useState<string[] | null>(null);
  const groups = directoryQuery.data?.groups ?? [];
  const selectedGroupIds = draftGroupIds ?? currentGroupIds;
  const loadError = getErrorMessageOrNull(directoryQuery.error);
  const loading = directoryQuery.isPending;
  const writeDisabled = busy || loading || groupsMutation.isPending;
  const unchanged =
    selectedGroupIds.length === currentGroupIds.length &&
    selectedGroupIds.every((groupId) => currentGroupIds.includes(groupId));

  const toggleGroup = (groupId: string, checked: boolean) => {
    setDraftGroupIds(
      checked
        ? [...selectedGroupIds, groupId]
        : selectedGroupIds.filter((currentGroupId) => currentGroupId !== groupId),
    );
  };

  const save = async () => {
    try {
      await groupsMutation.mutateAsync({ accountId, groupIds: selectedGroupIds });
      setDraftGroupIds(null);
    } catch {
      // The mutation hook reports the error; keep the unsaved draft selected.
    }
  };

  return (
    <section className="space-y-2 rounded-lg border p-3" aria-busy={loading}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {t("accounts.detail.groupsLabel")}
        </h3>
        <span className="text-xs text-muted-foreground">
          {selectedGroupIds.length === 0
            ? t("accounts.detail.noGroupsSelected")
            : t("accounts.detail.groupsSelected", { count: selectedGroupIds.length })}
        </span>
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground">{t("accounts.detail.groupsLoading")}</p>
      ) : loadError ? (
        <AlertMessage variant="error">{loadError}</AlertMessage>
      ) : groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("accounts.detail.noAccountGroups")}</p>
      ) : (
        <fieldset className="max-h-40 space-y-2 overflow-y-auto" disabled={writeDisabled}>
          {groups.map((group) => {
            const checkboxId = `account-${accountId}-group-${group.id}`;
            return (
              <label key={group.id} htmlFor={checkboxId} className="flex items-center gap-2 text-sm">
                <Checkbox
                  id={checkboxId}
                  checked={selectedGroupIds.includes(group.id)}
                  onCheckedChange={(checked) => toggleGroup(group.id, checked === true)}
                />
                <span className="min-w-0 truncate">{group.name}</span>
              </label>
            );
          })}
        </fieldset>
      )}

      <Button
        type="button"
        size="sm"
        className="h-8"
        disabled={writeDisabled || loadError !== null || unchanged}
        onClick={() => void save()}
      >
        {t("accounts.detail.saveGroups")}
      </Button>
    </section>
  );
}
