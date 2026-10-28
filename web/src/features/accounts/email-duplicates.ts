/** Legacy placeholder for accounts imported without an e-mail claim. */
const PLACEHOLDER_EMAIL = "unknown@example.com";

function comparableEmail(email: string | null | undefined): string | null {
  const normalized = email?.trim().toLowerCase() ?? "";
  return normalized !== "" && normalized !== PLACEHOLDER_EMAIL ? normalized : null;
}

/**
 * Marks every row whose real e-mail appears more than once in the same list,
 * so views can add a compact account id to otherwise identical labels. The
 * marker is a property of the rendered list, so it is derived here from each
 * parsed response instead of being sent by the server.
 */
export function markDuplicateEmails<T extends { email?: string | null }>(
  accounts: T[],
): Array<T & { isEmailDuplicate?: boolean }> {
  const counts = new Map<string, number>();
  for (const account of accounts) {
    const email = comparableEmail(account.email);
    if (email) {
      counts.set(email, (counts.get(email) ?? 0) + 1);
    }
  }
  return accounts.map((account) => {
    const email = comparableEmail(account.email);
    return { ...account, isEmailDuplicate: email !== null && (counts.get(email) ?? 0) > 1 };
  });
}
