import { useTranslation } from "react-i18next";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAccountGroups } from "@/features/account-groups/hooks/use-account-groups";
import { getErrorMessageOrNull } from "@/utils/errors";

export const NO_GROUP_VALUE = "__no_group__";

export type GroupSelectProps = {
  value: string | null;
  onChange: (value: string | null) => void;
  id?: string;
  disabled?: boolean;
};

export function GroupSelect({ value, onChange, id, disabled = false }: GroupSelectProps) {
  const { t } = useTranslation();
  const { groupsQuery } = useAccountGroups();
  const groups = groupsQuery.data ?? [];
  const loadError = getErrorMessageOrNull(groupsQuery.error);
  const pending = groupsQuery.isPending;
  const selectedGroup = value === null ? null : groups.find((group) => group.id === value) ?? null;
  const label =
    loadError
    ?? (pending
      ? t("apiKeys.accountSelect.loading")
      : value === null
        ? t("apiKeys.form.noGroup")
        : selectedGroup?.name ?? value);
  const unavailable = pending || loadError !== null;

  return (
    <Select
      value={value ?? NO_GROUP_VALUE}
      onValueChange={(next) => onChange(next === NO_GROUP_VALUE ? null : next)}
      disabled={disabled || unavailable}
    >
      <SelectTrigger id={id}>
        <SelectValue placeholder={t("apiKeys.form.noGroup")}>{label}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={NO_GROUP_VALUE}>{t("apiKeys.form.noGroup")}</SelectItem>
        {groups.map((group) => (
          <SelectItem key={group.id} value={group.id}>
            {group.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
