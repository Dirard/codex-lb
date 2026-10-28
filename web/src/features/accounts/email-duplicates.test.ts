import { describe, expect, it } from "vitest";

import { markDuplicateEmails } from "@/features/accounts/email-duplicates";
import { AccountsResponseSchema } from "@/features/accounts/schemas";
import { DashboardOverviewSchema } from "@/features/dashboard/schemas";
import { createAccountSummary, createDashboardOverview } from "@/test/mocks/factories";

function flags(emails: Array<string | null>) {
  return markDuplicateEmails(emails.map((email) => ({ email }))).map((row) => row.isEmailDuplicate);
}

describe("markDuplicateEmails", () => {
  it("flags every row of a repeated real e-mail, ignoring case and whitespace", () => {
    expect(flags(["a@example.com", " A@Example.com ", "b@example.com"])).toEqual([true, true, false]);
  });

  it("never flags blank or placeholder e-mails", () => {
    expect(flags(["", "  ", null, "unknown@example.com", "UNKNOWN@example.com"])).toEqual([
      false, false, false, false, false,
    ]);
  });

  it("keeps unique e-mails unflagged", () => {
    expect(flags(["one@example.com", "two@example.com"])).toEqual([false, false]);
  });
});

describe("account list responses", () => {
  // Go does not send isEmailDuplicate; parsing each list derives it.
  const twins = [
    createAccountSummary({ accountId: "acc-a", email: "twin@example.com", displayName: "twin@example.com" }),
    createAccountSummary({ accountId: "acc-b", email: "twin@example.com", displayName: "twin@example.com" }),
    createAccountSummary({ accountId: "acc-c", email: "solo@example.com", displayName: "solo@example.com" }),
  ].map((account) => ({ ...account, isEmailDuplicate: undefined }));

  it("derives the marker for GET /api/accounts", () => {
    const parsed = AccountsResponseSchema.parse({ accounts: twins });
    expect(parsed.accounts.map((account) => account.isEmailDuplicate)).toEqual([true, true, false]);
  });

  it("derives the marker for dashboard overview accounts", () => {
    const parsed = DashboardOverviewSchema.parse({ ...createDashboardOverview(), accounts: twins });
    expect(parsed.accounts.map((account) => account.isEmailDuplicate)).toEqual([true, true, false]);
  });
});
