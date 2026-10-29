import { Eye, EyeOff, LogOut, Menu } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, NavLink } from "react-router-dom";

import { CodexLogo } from "@/components/brand/codex-logo";
import { LanguageToggle, LanguageToggleMobile } from "@/components/layout/language-toggle";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { accountsBadge } from "@/components/layout/shell-status";
import { useShellStatus } from "@/components/layout/use-shell-status";
import { GlobalSearch } from "@/features/search/components/global-search";
import { usePrivacyStore } from "@/hooks/use-privacy";
import { cn } from "@/lib/utils";

// Operator order: health first, then the entities, then reporting and
// configuration. Groups and Keys open the existing group settings and APIs
// pages until their own pages exist.
const CORE_NAV_ITEMS = [
  { to: "/overview", labelKey: "nav.overview" },
  { to: "/groups", labelKey: "nav.groups" },
  { to: "/accounts", labelKey: "nav.accounts" },
  { to: "/keys", labelKey: "nav.keys" },
  { to: "/reports", labelKey: "nav.reports" },
  { to: "/automations", labelKey: "nav.automations" },
  { to: "/settings", labelKey: "nav.settings" },
] as const;


export type AppHeaderProps = {
  onLogout: () => void;
  showLogout?: boolean;
  className?: string;
};

export function AppHeader({
  onLogout,
  showLogout = true,
  className,
}: AppHeaderProps) {
  const { t } = useTranslation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const blurred = usePrivacyStore((s) => s.blurred);
  const togglePrivacy = usePrivacyStore((s) => s.toggle);
  const PrivacyIcon = blurred ? EyeOff : Eye;
  const { data: shell } = useShellStatus();
  const badge = accountsBadge(shell);
  const badgeTitle = badge?.tone === "reauth" ? t("nav.reauthBadge", { count: shell?.reauthRequiredAccounts ?? 0 }) : undefined;
  const privacyLabel = blurred ? t("nav.showEmails") : t("nav.hideEmails");

  return (
    <header
      className={cn(
        "sticky top-0 z-20 border-b border-white/[0.08] bg-background/50 px-4 py-2.5 shadow-[0_1px_12px_rgba(0,0,0,0.06)] backdrop-blur-xl backdrop-saturate-[1.8] supports-[backdrop-filter]:bg-background/40 dark:shadow-[0_1px_12px_rgba(0,0,0,0.25)]",
        className,
      )}
    >
      <div className="mx-auto flex w-full max-w-[1500px] items-center justify-between gap-4">
        {/* Brand */}
        <Link
          to="/overview"
          className="flex min-w-0 flex-1 items-center gap-2.5 rounded-lg no-underline transition-colors hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-gradient-to-br from-primary/15 to-primary/5">
            <CodexLogo size={20} className="text-primary" />
          </div>
          <div className="min-w-0">
            <p className="truncate text-sm font-semibold tracking-tight">Codex LB</p>
          </div>
        </Link>

        {/* Desktop nav pills */}
        <nav className="hidden items-center rounded-lg border border-border/50 bg-muted/40 p-0.5 lg:flex">
          {CORE_NAV_ITEMS.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                cn(
                  "relative inline-flex h-7 items-center rounded-md px-3.5 text-xs leading-none font-medium transition-colors duration-200",
                  isActive
                    ? "bg-background text-foreground shadow-[var(--shadow-xs)]"
                    : "text-muted-foreground hover:text-foreground",
                )
              }
            >
              <span className="relative inline-flex items-center">
                {t(item.labelKey)}
                {item.to === "/accounts" && badge ? (
                  <span
                    data-testid="accounts-badge"
                    title={badgeTitle}
                    className={cn(
                      "absolute -top-2 -right-4 z-10 grid h-4 min-w-[1rem] place-items-center rounded-full px-1 text-[10px] font-medium",
                      badge.tone === "reauth" ? "bg-destructive text-white" : "bg-primary text-primary-foreground",
                    )}
                  >
                    {badge.label}
                  </span>
                ) : null}
              </span>
            </NavLink>
          ))}
        </nav>

        {/* Actions */}
        <div className="flex flex-1 items-center justify-end gap-1.5">
          <GlobalSearch />
          <LanguageToggle />
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={togglePrivacy}
            aria-label={privacyLabel}
            className="press-scale hidden h-8 w-8 rounded-lg text-muted-foreground hover:text-foreground sm:inline-flex"
          >
            <PrivacyIcon className="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
          {showLogout && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={onLogout}
              className="press-scale hidden h-8 gap-1.5 rounded-lg text-xs text-muted-foreground hover:text-foreground sm:inline-flex"
            >
              <LogOut className="h-3.5 w-3.5" aria-hidden="true" />
              {t("common.logout")}
            </Button>
          )}
          {/* Mobile menu */}
          <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
            <SheetTrigger asChild>
              <Button type="button" size="icon" variant="ghost" aria-label={t("nav.openMenu")} className="h-8 w-8 rounded-lg lg:hidden">
                <Menu className="h-4 w-4" />
              </Button>
            </SheetTrigger>
            <SheetContent side="right" className="w-72">
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2.5">
                  <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary/10">
                    <CodexLogo size={16} className="text-primary" />
                  </div>
                  <span className="text-sm font-semibold">Codex LB</span>
                </SheetTitle>
              </SheetHeader>
              <nav className="flex flex-col gap-0.5 px-4 pt-2">
                {CORE_NAV_ITEMS.map((item) => (
                  <NavLink key={item.to} to={item.to} onClick={() => setMobileOpen(false)}>
                    {({ isActive }) => (
                      <span
                        className={cn(
                          "relative block w-full rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors",
                          isActive
                            ? "bg-primary/10 text-primary"
                            : "text-muted-foreground hover:bg-muted hover:text-foreground",
                        )}
                      >
                        {t(item.labelKey)}
                        {item.to === "/accounts" && badge ? (
                          <span
                            className={cn(
                              "absolute right-2 top-1 z-10 grid h-5 min-w-[1.25rem] place-items-center rounded-full px-1 text-[10px] font-medium",
                              badge.tone === "reauth" ? "bg-destructive text-white" : "bg-primary text-primary-foreground",
                            )}
                          >
                            {badge.label}
                          </span>
                        ) : null}
                      </span>
                    )}
                  </NavLink>
                ))}
                <div className="my-2 h-px bg-border" />
                <button
                  type="button"
                  className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  onClick={togglePrivacy}
                >
                  <PrivacyIcon className="h-3.5 w-3.5" aria-hidden="true" />
                  {privacyLabel}
                </button>
                <div className="my-2 h-px bg-border" />
                <LanguageToggleMobile />
                {showLogout && (
                  <button
                    type="button"
                    className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                    onClick={() => {
                      setMobileOpen(false);
                      onLogout();
                    }}
                  >
                    <LogOut className="h-3.5 w-3.5" aria-hidden="true" />
                    {t("common.logout")}
                  </button>
                )}
              </nav>
            </SheetContent>
          </Sheet>
        </div>
      </div>
    </header>
  );
}
