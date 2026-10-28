import type { Directory } from "@/features/search/api";

/** What the request and conversation tables need to label an account id. */
export type AccountLabelSource = {
  accountId: string;
  displayName?: string | null;
  email?: string | null;
};

/** Account labels from the directory: alias, else e-mail. */
export function accountLabelSources(directory: Directory | undefined): AccountLabelSource[] {
  return (directory?.accounts ?? []).map((account) => ({
    accountId: account.id,
    displayName: account.label,
    email: account.email,
  }));
}
