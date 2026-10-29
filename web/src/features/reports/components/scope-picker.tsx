import { Check, ChevronDown, Search, X } from "lucide-react";
import { useEffect, useId, useMemo, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { Directory } from "@/features/search/api";
import { cn } from "@/lib/utils";

import { EMPTY_SCOPE, isScopeEmpty, type ReportScope } from "../report-url";

/** At most this many groups and keys render at once; typing narrows the rest. */
export const SCOPE_PICKER_ROW_LIMIT = 100;
/** Chips shown before the rest collapse into "+n more". */
export const SCOPE_CHIP_LIMIT = 6;

type ScopeOption =
  | { kind: "all"; id: string }
  | { kind: "ungrouped"; id: string; count: number }
  | { kind: "group"; id: string; name: string; keyCount: number }
  | { kind: "key"; id: string; name: string; prefix: string; groupName: string | null };

type IndexedGroup = { id: string; name: string; keyCount: number; haystack: string };
type IndexedKey = { id: string; name: string; prefix: string; groupName: string | null; haystack: string };

type ScopeIndex = {
  groups: IndexedGroup[];
  keys: IndexedKey[];
  groupsById: Map<string, IndexedGroup>;
  keysById: Map<string, IndexedKey>;
  ungroupedCount: number;
};

function buildIndex(directory: Directory | undefined): ScopeIndex {
  const groups: IndexedGroup[] = (directory?.groups ?? [])
    .map((group) => ({ id: group.id, name: group.name, keyCount: group.keyCount, haystack: group.name.toLowerCase() }))
    .sort((a, b) => a.name.localeCompare(b.name));
  const groupNames = new Map(groups.map((group) => [group.id, group.name]));
  let ungroupedCount = 0;
  const keys: IndexedKey[] = (directory?.keys ?? [])
    .map((key) => {
      const groupName = key.groupId ? (groupNames.get(key.groupId) ?? null) : null;
      if (!key.groupId) {
        ungroupedCount += 1;
      }
      return {
        id: key.id,
        name: key.name,
        prefix: key.prefix,
        groupName,
        haystack: `${key.name}\u0000${key.prefix}\u0000${groupName ?? ""}`.toLowerCase(),
      };
    })
    .sort((a, b) => a.name.localeCompare(b.name));
  return {
    groups,
    keys,
    groupsById: new Map(groups.map((group) => [group.id, group])),
    keysById: new Map(keys.map((key) => [key.id, key])),
    ungroupedCount,
  };
}

export type ScopePickerProps = {
  directory: Directory | undefined;
  loading?: boolean;
  scope: ReportScope;
  onChange: (scope: ReportScope) => void;
};

function toggle(values: string[], id: string): string[] {
  return values.includes(id) ? values.filter((value) => value !== id) : [...values, id];
}

/**
 * The searchable multi-select over the directory's groups and keys (R32).
 * Selecting a group means its current member keys, which the server
 * resolves; "All keys" clears the selection. Matches are filtered in memory
 * and at most 100 rows render, so hundreds of keys stay responsive.
 */
export function ScopePicker({ directory, loading = false, scope, onChange }: ScopePickerProps) {
  const { t } = useTranslation();
  const listboxId = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [chipsExpanded, setChipsExpanded] = useState(false);
  const index = useMemo(() => buildIndex(directory), [directory]);
  const selectedGroups = useMemo(() => new Set(scope.groupIds), [scope.groupIds]);
  const selectedKeys = useMemo(() => new Set(scope.keyIds), [scope.keyIds]);
  const allLabel = t("reports.scope.allKeys");
  const ungroupedLabel = t("reports.ungroupedKeys");

  const { options, total } = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const special: ScopeOption[] = [];
    if (!needle || allLabel.toLowerCase().includes(needle)) {
      special.push({ kind: "all", id: "all" });
    }
    if (!needle || ungroupedLabel.toLowerCase().includes(needle)) {
      special.push({ kind: "ungrouped", id: "ungrouped", count: index.ungroupedCount });
    }
    const groups = needle ? index.groups.filter((group) => group.haystack.includes(needle)) : index.groups;
    const keys = needle ? index.keys.filter((key) => key.haystack.includes(needle)) : index.keys;
    const shownGroups = groups.slice(0, SCOPE_PICKER_ROW_LIMIT);
    const shownKeys = keys.slice(0, Math.max(0, SCOPE_PICKER_ROW_LIMIT - shownGroups.length));
    return {
      options: [
        ...special,
        ...shownGroups.map((group): ScopeOption => ({ kind: "group", id: group.id, name: group.name, keyCount: group.keyCount })),
        ...shownKeys.map(
          (key): ScopeOption => ({ kind: "key", id: key.id, name: key.name, prefix: key.prefix, groupName: key.groupName }),
        ),
      ],
      total: groups.length + keys.length,
    };
  }, [allLabel, index, query, ungroupedLabel]);
  const shownMatches = options.filter((option) => option.kind === "group" || option.kind === "key").length;
  const activeIndex = Math.min(active, Math.max(0, options.length - 1));
  const optionId = (position: number) => `${listboxId}-option-${position}`;

  useEffect(() => {
    if (open) {
      document.getElementById(`${listboxId}-option-${activeIndex}`)?.scrollIntoView({ block: "nearest" });
    }
  }, [activeIndex, listboxId, open]);

  const isSelected = (option: ScopeOption): boolean => {
    switch (option.kind) {
      case "all":
        return isScopeEmpty(scope);
      case "ungrouped":
        return scope.ungrouped;
      case "group":
        return selectedGroups.has(option.id);
      case "key":
        return selectedKeys.has(option.id);
    }
  };

  const choose = (option: ScopeOption | undefined) => {
    if (!option) {
      return;
    }
    switch (option.kind) {
      case "all":
        onChange(EMPTY_SCOPE);
        return;
      case "ungrouped":
        onChange({ ...scope, ungrouped: !scope.ungrouped });
        return;
      case "group":
        onChange({ ...scope, groupIds: toggle(scope.groupIds, option.id) });
        return;
      case "key":
        onChange({ ...scope, keyIds: toggle(scope.keyIds, option.id) });
        return;
    }
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive(Math.min(activeIndex + 1, Math.max(0, options.length - 1)));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive(Math.max(0, activeIndex - 1));
    } else if (event.key === "Home") {
      event.preventDefault();
      setActive(0);
    } else if (event.key === "End") {
      event.preventDefault();
      setActive(Math.max(0, options.length - 1));
    } else if (event.key === "Enter") {
      event.preventDefault();
      choose(options[activeIndex]);
    }
  };

  const chips = useMemo(() => {
    const items: { id: string; label: string; detail: string; remove: ReportScope }[] = [];
    for (const id of scope.groupIds) {
      const group = index.groupsById.get(id);
      items.push({
        id: `group:${id}`,
        label: group?.name ?? id,
        detail: group ? t("reports.scope.keyCount", { count: group.keyCount }) : "",
        remove: { ...scope, groupIds: scope.groupIds.filter((value) => value !== id) },
      });
    }
    if (scope.ungrouped) {
      items.push({
        id: "ungrouped",
        label: ungroupedLabel,
        detail: directory ? t("reports.scope.keyCount", { count: index.ungroupedCount }) : "",
        remove: { ...scope, ungrouped: false },
      });
    }
    for (const id of scope.keyIds) {
      const key = index.keysById.get(id);
      items.push({
        id: `key:${id}`,
        label: key?.name ?? id,
        detail: key?.prefix ?? "",
        remove: { ...scope, keyIds: scope.keyIds.filter((value) => value !== id) },
      });
    }
    return items;
  }, [directory, index, scope, t, ungroupedLabel]);
  const visibleChips = chipsExpanded ? chips : chips.slice(0, SCOPE_CHIP_LIMIT);
  const hiddenChips = chips.length - visibleChips.length;
  const summary = chips.length === 0 ? allLabel : t("reports.scope.selected", { count: chips.length });

  const renderOption = ({ option, current }: { option: ScopeOption; current: number }) => {
    const selected = isSelected(option);
    let label: string;
    let detail: string | null = null;
    switch (option.kind) {
      case "all":
        label = allLabel;
        break;
      case "ungrouped":
        label = ungroupedLabel;
        detail = directory ? t("reports.scope.keyCount", { count: option.count }) : null;
        break;
      case "group":
        label = option.name;
        detail = t("reports.scope.keyCount", { count: option.keyCount });
        break;
      case "key":
        label = option.prefix ? `${option.name} · ${option.prefix}` : option.name;
        detail = option.groupName ?? ungroupedLabel;
        break;
    }
    return (
      <div
        key={`${option.kind}:${option.id}`}
        id={optionId(current)}
        role="option"
        aria-selected={selected}
        onMouseDown={(event) => event.preventDefault()}
        onMouseEnter={() => setActive(current)}
        onClick={() => {
          setActive(current);
          choose(option);
        }}
        className={cn(
          "flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm",
          current === activeIndex ? "bg-accent text-accent-foreground" : "hover:bg-muted",
        )}
      >
        <span
          aria-hidden="true"
          className={cn(
            "flex h-4 w-4 shrink-0 items-center justify-center rounded border",
            selected ? "border-primary bg-primary text-primary-foreground" : "border-input",
          )}
        >
          {selected ? <Check className="h-3 w-3" /> : null}
        </span>
        <span className="truncate">{label}</span>
        {detail ? <span className="ml-auto truncate pl-2 text-xs text-muted-foreground">{detail}</span> : null}
      </div>
    );
  };

  const positioned = options.map((option, current) => ({ option, current }));
  const special = positioned.filter(({ option }) => option.kind === "all" || option.kind === "ungrouped");
  const groupOptions = positioned.filter(({ option }) => option.kind === "group");
  const keyOptions = positioned.filter(({ option }) => option.kind === "key");

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
      <Popover
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) {
            setQuery("");
            setActive(0);
          }
        }}
      >
        <PopoverTrigger asChild>
          <Button type="button" variant="outline" size="sm" className="h-8 max-w-64 justify-between gap-2 text-xs">
            <span className="sr-only">{t("reports.scope.label")}: </span>
            <span className="truncate">{summary}</span>
            <ChevronDown className="h-3.5 w-3.5 opacity-60" aria-hidden="true" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[min(26rem,calc(100vw-2rem))] p-0">
          <div className="flex items-center gap-2 border-b px-2">
            <Search className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            <Input
              autoFocus
              value={query}
              role="combobox"
              aria-expanded="true"
              aria-controls={listboxId}
              aria-activedescendant={options.length > 0 ? optionId(activeIndex) : undefined}
              aria-autocomplete="list"
              aria-label={t("reports.scope.search")}
              placeholder={t("reports.scope.searchPlaceholder")}
              className="h-9 border-0 px-1 shadow-none focus-visible:ring-0"
              onChange={(event) => {
                setQuery(event.target.value);
                setActive(0);
              }}
              onKeyDown={onKeyDown}
            />
          </div>
          <div
            id={listboxId}
            role="listbox"
            aria-multiselectable="true"
            aria-label={t("reports.scope.label")}
            className="max-h-80 overflow-y-auto p-1"
          >
            {special.length > 0 ? <div role="group">{special.map(renderOption)}</div> : null}
            {groupOptions.length > 0 ? (
              <div role="group" aria-label={t("reports.groups.title")}>
                <p className="px-2 pt-2 pb-1 text-[11px] font-medium tracking-wider text-muted-foreground uppercase">
                  {t("reports.groups.title")}
                </p>
                {groupOptions.map(renderOption)}
              </div>
            ) : null}
            {keyOptions.length > 0 ? (
              <div role="group" aria-label={t("reports.keys.title")}>
                <p className="px-2 pt-2 pb-1 text-[11px] font-medium tracking-wider text-muted-foreground uppercase">
                  {t("reports.keys.title")}
                </p>
                {keyOptions.map(renderOption)}
              </div>
            ) : null}
            {loading && !directory ? (
              <p className="px-3 py-4 text-center text-xs text-muted-foreground">{t("common.loading")}</p>
            ) : null}
            {directory && query.trim() && options.length === 0 ? (
              <p className="px-3 py-4 text-center text-xs text-muted-foreground">{t("reports.scope.noMatches")}</p>
            ) : null}
          </div>
          {total > shownMatches ? (
            <p className="border-t px-3 py-2 text-xs text-muted-foreground">
              {t("reports.scope.truncated", { shown: shownMatches, total })}
            </p>
          ) : null}
        </PopoverContent>
      </Popover>
      {visibleChips.map((chip) => (
        <span
          key={chip.id}
          data-testid="scope-chip"
          className="inline-flex h-7 max-w-56 items-center gap-1 rounded-full border bg-muted/40 pr-1 pl-2.5 text-xs"
        >
          <span className="truncate font-medium">{chip.label}</span>
          {chip.detail ? <span className="shrink-0 text-muted-foreground">{chip.detail}</span> : null}
          <button
            type="button"
            onClick={() => onChange(chip.remove)}
            aria-label={t("reports.scope.remove", { name: chip.label })}
            className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-accent-foreground"
          >
            <X className="h-3 w-3" aria-hidden="true" />
          </button>
        </span>
      ))}
      {hiddenChips > 0 ? (
        <Button type="button" variant="ghost" size="xs" onClick={() => setChipsExpanded(true)}>
          {t("reports.scope.more", { count: hiddenChips })}
        </Button>
      ) : null}
      {chipsExpanded && chips.length > SCOPE_CHIP_LIMIT ? (
        <Button type="button" variant="ghost" size="xs" onClick={() => setChipsExpanded(false)}>
          {t("reports.scope.less")}
        </Button>
      ) : null}
    </div>
  );
}
