import { Building2, FileSearch, KeyRound, Search, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { SpinnerBlock } from "@/components/ui/spinner";
import { useDirectory } from "@/features/search/hooks/use-directory";
import { searchDirectory, type SearchResult } from "@/features/search/search-directory";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { cn } from "@/lib/utils";

const KIND_ICONS = {
  request: FileSearch,
  key: KeyRound,
  group: Users,
  account: Building2,
} as const;

const KIND_ORDER: SearchResult["kind"][] = ["request", "key", "group", "account"];

function isMac(): boolean {
  return typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
}

/**
 * Global search over the directory: opened with the header button or
 * Ctrl/Cmd+K, it matches keys, groups and accounts and opens a request by id.
 */
export function GlobalSearch() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const blurred = usePrivacyStore((s) => s.blurred);
  const directoryQuery = useDirectory(open);
  const results = useMemo(() => searchDirectory(directoryQuery.data, query), [directoryQuery.data, query]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen((value) => !value);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const close = () => {
    setOpen(false);
    setQuery("");
    setActive(0);
  };

  const select = (result: SearchResult | undefined) => {
    if (!result) {
      return;
    }
    close();
    navigate(result.to);
  };

  const grouped = KIND_ORDER.map((kind) => ({ kind, items: results.filter((result) => result.kind === kind) })).filter(
    (group) => group.items.length > 0,
  );
  const ordered = grouped.flatMap((group) => group.items);
  const shortcut = isMac() ? "⌘K" : "Ctrl K";

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(true)}
        aria-label={t("search.open")}
        className="hidden h-8 gap-2 rounded-lg px-2.5 text-xs text-muted-foreground md:inline-flex"
      >
        <Search className="h-3.5 w-3.5" aria-hidden="true" />
        <span>{t("search.placeholderShort")}</span>
        <kbd className="rounded border bg-muted px-1 font-mono text-[10px]">{shortcut}</kbd>
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        onClick={() => setOpen(true)}
        aria-label={t("search.open")}
        className="h-8 w-8 rounded-lg md:hidden"
      >
        <Search className="h-4 w-4" aria-hidden="true" />
      </Button>
      <Dialog open={open} onOpenChange={(next) => (next ? setOpen(true) : close())}>
        <DialogContent className="top-[20%] max-w-xl translate-y-0 gap-0 p-0" showCloseButton={false}>
          <DialogTitle className="sr-only">{t("search.title")}</DialogTitle>
          <DialogDescription className="sr-only">{t("search.description")}</DialogDescription>
          <div className="flex items-center gap-2 border-b px-3">
            <Search className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
            <Input
              autoFocus
              value={query}
              role="combobox"
              aria-expanded={ordered.length > 0}
              aria-controls="global-search-results"
              aria-label={t("search.title")}
              placeholder={t("search.placeholder")}
              className="h-11 border-0 shadow-none focus-visible:ring-0"
              onChange={(event) => {
                setQuery(event.target.value);
                setActive(0);
              }}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") {
                  event.preventDefault();
                  setActive((value) => Math.min(value + 1, Math.max(0, ordered.length - 1)));
                } else if (event.key === "ArrowUp") {
                  event.preventDefault();
                  setActive((value) => Math.max(0, value - 1));
                } else if (event.key === "Enter") {
                  event.preventDefault();
                  select(ordered[active]);
                }
              }}
            />
          </div>
          <div id="global-search-results" role="listbox" aria-label={t("search.results")} className="max-h-96 overflow-y-auto p-1">
            {directoryQuery.isPending && query.trim() ? <SpinnerBlock /> : null}
            {query.trim() && !directoryQuery.isPending && ordered.length === 0 ? (
              <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t("search.noResults")}</p>
            ) : null}
            {!query.trim() ? <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t("search.hint")}</p> : null}
            {grouped.map((group) => (
              <div key={group.kind} role="group" aria-label={t(`search.kinds.${group.kind}`)}>
                <p className="px-2 pt-2 pb-1 text-[11px] font-medium tracking-wider text-muted-foreground uppercase">
                  {t(`search.kinds.${group.kind}`)}
                </p>
                {group.items.map((result) => {
                  const index = ordered.indexOf(result);
                  const Icon = KIND_ICONS[result.kind];
                  return (
                    <button
                      key={`${result.kind}:${result.id}`}
                      type="button"
                      role="option"
                      aria-selected={index === active}
                      onMouseEnter={() => setActive(index)}
                      onClick={() => select(result)}
                      className={cn(
                        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm",
                        index === active ? "bg-accent text-accent-foreground" : "hover:bg-muted",
                      )}
                    >
                      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                      <span
                        className={cn(
                          "truncate",
                          result.kind === "account" && result.isEmail && blurred ? "privacy-blur" : undefined,
                        )}
                      >
                        {result.kind === "request" ? t("search.openRequest", { id: result.title }) : result.title}
                      </span>
                      {result.detail ? (
                        <span
                          className={cn(
                            "ml-auto truncate font-mono text-xs text-muted-foreground",
                            result.kind === "account" && blurred ? "privacy-blur" : undefined,
                          )}
                        >
                          {result.detail}
                        </span>
                      ) : null}
                    </button>
                  );
                })}
              </div>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
