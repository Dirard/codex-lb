import { z } from "zod";

import { get } from "@/lib/api-client";

/**
 * The shell status the header and the status bar show on every admin page.
 * Pages with their own poll receive it inside that response and write it into
 * the shared query, so no page needs a second periodic request.
 */
export const ShellStatusSchema = z.object({
  ready: z.boolean(),
  lastSyncAt: z.iso.datetime({ offset: true }).nullable(),
  usageFoldedAt: z.iso.datetime({ offset: true }).nullable(),
  rollupsUsable: z.boolean(),
  reauthRequiredAccounts: z.number().int().nonnegative(),
  availableResetCredits: z.number().int().nonnegative(),
  showResetCreditBadges: z.boolean(),
  apiKeyAuthEnabled: z.boolean(),
  routingStrategy: z.string(),
  stickyThreadsEnabled: z.boolean(),
  preferEarlierResetAccounts: z.boolean(),
  preferEarlierResetWindow: z.string(),
});

export type ShellStatus = z.infer<typeof ShellStatusSchema>;

export const SHELL_STATUS_QUERY_KEY = ["shell", "status"] as const;

/** Pages without their own poll refresh the shell status at this interval. */
export const SHELL_STATUS_POLL_MS = 60_000;

/** Routes whose page poll carries the shell status. */
const PAGES_CARRYING_SHELL_STATUS = ["/overview"];

export function pageCarriesShellStatus(pathname: string): boolean {
  const normalized = pathname.replace(/\/+$/, "") || "/";
  return PAGES_CARRYING_SHELL_STATUS.includes(normalized);
}

export function getShellStatus() {
  return get("/api/dashboard/status", ShellStatusSchema);
}

/**
 * The Accounts badge: accounts needing re-login first, otherwise the
 * available reset credits when reset-credit badges are enabled.
 */
export function accountsBadge(shell: Pick<ShellStatus, "reauthRequiredAccounts" | "availableResetCredits" | "showResetCreditBadges"> | undefined):
  | { label: string; tone: "reauth" | "credits" }
  | null {
  if (!shell) {
    return null;
  }
  const cap = (value: number) => (value > 99 ? "99+" : String(value));
  if (shell.reauthRequiredAccounts > 0) {
    return { label: cap(shell.reauthRequiredAccounts), tone: "reauth" };
  }
  if (shell.showResetCreditBadges && shell.availableResetCredits > 0) {
    return { label: cap(shell.availableResetCredits), tone: "credits" };
  }
  return null;
}
