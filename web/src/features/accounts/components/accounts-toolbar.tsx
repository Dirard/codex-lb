import { ChevronDown, ChevronUp, Plus, Search } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ACCOUNT_SORT_OPTIONS, type AccountFilters, type AccountSortMode } from "@/features/accounts/sorting";
import { formatSlug } from "@/utils/formatters";

const STATUS_FILTER_OPTIONS = ["all", "active", "paused", "rate_limited", "quota_exceeded", "reauth_required", "deactivated"];

export type AccountsToolbarProps = {
  filters: AccountFilters;
  onFiltersChange: (filters: AccountFilters) => void;
  sortMode: AccountSortMode;
  onSortModeChange: (mode: AccountSortMode) => void;
  groups: { id: string; name: string }[];
  helpOpen: boolean;
  onHelpToggle: () => void;
  onAddAccount: () => void;
};

/**
 * The accounts toolbar: search by alias, e-mail or id, status and group
 * filters, sort, the Windows OAuth help toggle and Add account (use case 8).
 */
export function AccountsToolbar({
  filters,
  onFiltersChange,
  sortMode,
  onSortModeChange,
  groups,
  helpOpen,
  onHelpToggle,
  onAddAccount,
}: AccountsToolbarProps) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="accounts-toolbar">
      <div className="relative min-w-[12rem] flex-1">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground/60" aria-hidden />
        <Input
          placeholder={t("accounts.toolbar.searchPlaceholder")}
          aria-label={t("accounts.toolbar.searchPlaceholder")}
          value={filters.search}
          onChange={(event) => onFiltersChange({ ...filters, search: event.target.value })}
          className="h-8 pl-8"
        />
      </div>
      <Select value={filters.status} onValueChange={(status) => onFiltersChange({ ...filters, status })}>
        <SelectTrigger size="sm" className="w-full min-w-0 sm:w-40" aria-label={t("accounts.list.statusFilterAria")}>
          <SelectValue placeholder={t("accounts.list.statusPlaceholder")} />
        </SelectTrigger>
        <SelectContent>
          {STATUS_FILTER_OPTIONS.map((option) => (
            <SelectItem key={option} value={option}>
              {option === "all"
                ? t("accounts.list.allStatuses")
                : t(`accounts.statusFilters.${option}`, { defaultValue: formatSlug(option) })}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={filters.group} onValueChange={(group) => onFiltersChange({ ...filters, group })}>
        <SelectTrigger size="sm" className="w-full min-w-0 sm:w-40" aria-label={t("accounts.toolbar.groupFilterAria")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">{t("accounts.toolbar.allGroups")}</SelectItem>
          <SelectItem value="ungrouped">{t("accounts.toolbar.ungrouped")}</SelectItem>
          {groups.map((group) => (
            <SelectItem key={group.id} value={group.id}>
              {group.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={sortMode} onValueChange={(mode) => onSortModeChange(mode as AccountSortMode)}>
        <SelectTrigger size="sm" className="w-full min-w-0 sm:w-44" aria-label={t("accounts.list.sortAria")}>
          <SelectValue placeholder={t("accounts.list.sortPlaceholder")} />
        </SelectTrigger>
        <SelectContent>
          {ACCOUNT_SORT_OPTIONS.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {t(`accounts.sort.${option.value}`, { defaultValue: option.label })}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button type="button" variant="link" size="sm" className="h-auto px-1 text-xs" onClick={onHelpToggle} aria-expanded={helpOpen}>
        {t("accounts.list.needHelp")}
        {helpOpen ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
      </Button>
      <Button type="button" size="sm" className="gap-1.5" onClick={onAddAccount}>
        <Plus className="h-3.5 w-3.5" />
        {t("accounts.list.addAccount")}
      </Button>
    </div>
  );
}
