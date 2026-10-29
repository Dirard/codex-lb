import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { beforeEach, describe, expect, it } from "vitest";

import { AccountsPage } from "@/features/accounts/components/accounts-page";
import { createAccountGroup, createAccountSummary } from "@/test/mocks/factories";
import { createDirectory } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const groups = [
  createAccountGroup({ id: "grp_team", name: "Team pool", accountIds: ["acc_team"] }),
  createAccountGroup({ id: "grp_spare", name: "Spare pool", accountIds: [] }),
];

const accounts = [
  createAccountSummary({ accountId: "acc_team", email: "team@example.com", displayName: "team@example.com", groups: ["grp_team"], status: "active" }),
  createAccountSummary({ accountId: "acc_paused", email: "paused@example.com", displayName: "paused@example.com", status: "paused" }),
  createAccountSummary({ accountId: "acc_reauth", email: "reauth@example.com", alias: "Needs login", displayName: "Needs login", status: "reauth_required" }),
  createAccountSummary({
    accountId: "acc_limited",
    email: "limited@example.com",
    displayName: "limited@example.com",
    status: "rate_limited",
    usage: { primaryRemainingPercent: 0, secondaryRemainingPercent: 40 },
    availableResetCredits: 2,
  }),
];

function mockList(list = accounts) {
  server.use(
    http.get("/api/accounts", () => HttpResponse.json({ accounts: list })),
    http.get("/api/directory", () => HttpResponse.json(createDirectory(list, [], groups))),
  );
}

function rowIds(): string[] {
  return screen.getAllByTestId("accounts-table-row").map((row) => row.getAttribute("data-account-id") ?? "");
}

describe("AccountsPage", () => {
  beforeEach(() => {
    window.history.pushState({}, "", "/accounts");
  });

  it("lists every account with problems first and filters by search, status and group", async () => {
    const user = userEvent.setup();
    mockList();
    renderWithProviders(<AccountsPage />);

    await waitFor(() => expect(rowIds()).toEqual(["acc_reauth", "acc_limited", "acc_paused", "acc_team"]));
    const limited = screen.getAllByTestId("accounts-table-row")[1];
    expect(within(limited).getByText(/^Blocked, resets /)).toBeInTheDocument();
    expect(within(limited).getByText("2")).toBeInTheDocument();
    expect(await screen.findByText("Team pool")).toBeInTheDocument();
    // Accounts needing re-login offer Re-login instead of Pause and no priority.
    const reauth = screen.getAllByTestId("accounts-table-row")[0];
    expect(within(reauth).getByRole("button", { name: "Re-login" })).toBeInTheDocument();
    expect(within(reauth).queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    expect(within(reauth).queryByRole("combobox", { name: /Priority/ })).not.toBeInTheDocument();

    await user.type(screen.getByRole("textbox", { name: "Search alias, e-mail or id" }), "NEEDS");
    expect(rowIds()).toEqual(["acc_reauth"]);
    await user.clear(screen.getByRole("textbox", { name: "Search alias, e-mail or id" }));

    await user.click(screen.getByRole("combobox", { name: "Filter accounts by group" }));
    await user.click(await screen.findByRole("option", { name: "Team pool" }));
    expect(rowIds()).toEqual(["acc_team"]);

    await user.click(screen.getByRole("combobox", { name: "Filter accounts by group" }));
    await user.click(await screen.findByRole("option", { name: "Ungrouped" }));
    expect(rowIds()).toEqual(["acc_reauth", "acc_limited", "acc_paused"]);

    await user.click(screen.getByRole("combobox", { name: "Sort accounts" }));
    await user.click(await screen.findByRole("option", { name: "Name (A-Z)" }));
    expect(rowIds()).toEqual(["acc_limited", "acc_reauth", "acc_paused"]);
  });

  it("shows a skeleton, then a retryable error that keeps the previous rows", async () => {
    const user = userEvent.setup();
    let fail = false;
    server.use(
      http.get("/api/accounts", () =>
        fail
          ? HttpResponse.json({ error: { code: "internal_error", message: "database is busy" } }, { status: 500 })
          : HttpResponse.json({ accounts }),
      ),
      http.get("/api/directory", () => HttpResponse.json(createDirectory(accounts, [], groups))),
    );
    const view = renderWithProviders(<AccountsPage />);

    expect(screen.getByTestId("accounts-skeleton")).toBeInTheDocument();
    await waitFor(() => expect(rowIds()).toHaveLength(4));

    fail = true;
    await view.queryClient.refetchQueries({ queryKey: ["accounts", "list"] });
    expect(await screen.findByText(/Could not refresh the accounts \(database is busy\)/)).toBeInTheDocument();
    expect(rowIds()).toHaveLength(4);

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText(/Could not refresh the accounts/)).not.toBeInTheDocument());
  });

  it("offers Add account when there are no accounts", async () => {
    const user = userEvent.setup();
    mockList([]);
    renderWithProviders(<AccountsPage />);

    expect(await screen.findByText("No accounts yet")).toBeInTheDocument();
    const addButtons = screen.getAllByRole("button", { name: "Add account" });
    expect(addButtons).toHaveLength(2);
    await user.click(addButtons[1]);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Import")).toBeInTheDocument();
  });

  it("opens the detail sheet from a row and from ?selected, and closes it", async () => {
    const user = userEvent.setup();
    mockList();
    window.history.pushState({}, "", "/accounts?selected=acc_paused");
    renderWithProviders(<AccountsPage />);

    const sheet = await screen.findByTestId("account-detail-sheet");
    expect(within(sheet).getByRole("heading", { name: "paused@example.com" })).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByTestId("account-detail-sheet")).not.toBeInTheDocument());
    expect(window.location.search).toBe("");

    await user.click(screen.getByRole("button", { name: "team@example.com" }));
    expect(await screen.findByTestId("account-detail-sheet")).toBeInTheDocument();
    expect(window.location.search).toBe("?selected=acc_team");
  });

  it("probes from the row menu without a confirmation", async () => {
    const user = userEvent.setup();
    const probes: string[] = [];
    mockList();
    server.use(
      http.post("/api/accounts/:accountId/probe", ({ params }) => {
        probes.push(String(params.accountId));
        return HttpResponse.json({
          status: "probed", accountId: String(params.accountId), probeStatusCode: 200,
          primaryUsedPercentBefore: 10, primaryUsedPercentAfter: 9, secondaryUsedPercentBefore: 20,
          secondaryUsedPercentAfter: 19, accountStatusBefore: "active", accountStatusAfter: "active",
        });
      }),
    );
    renderWithProviders(<AccountsPage />);

    await user.click(await screen.findByRole("button", { name: "More actions for team@example.com" }));
    await user.click(await screen.findByRole("menuitem", { name: "Force probe" }));

    await waitFor(() => expect(probes).toEqual(["acc_team"]));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("confirms before resetting usage from the detail sheet", async () => {
    const user = userEvent.setup();
    const consumed: string[] = [];
    mockList();
    server.use(
      http.post("/api/accounts/:accountId/usage-reset-credits/consume", ({ params }) => {
        consumed.push(String(params.accountId));
        return HttpResponse.json({
          status: "reset", accountId: String(params.accountId), code: "reset", windowsReset: 1, usageWritten: true,
          primaryUsedPercentBefore: 100, primaryUsedPercentAfter: 0, secondaryUsedPercentBefore: 60,
          secondaryUsedPercentAfter: 60, accountStatusBefore: "rate_limited", accountStatusAfter: "active",
        });
      }),
    );
    window.history.pushState({}, "", "/accounts?selected=acc_limited");
    renderWithProviders(<AccountsPage />);

    const sheet = await screen.findByTestId("account-detail-sheet");
    await user.click(within(sheet).getByRole("button", { name: "Reset usage" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Reset usage" });
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(consumed).toEqual([]);

    await user.click(within(sheet).getByRole("button", { name: "Reset usage" }));
    await user.click(within(await screen.findByRole("alertdialog", { name: "Reset usage" })).getByRole("button", { name: "Reset" }));
    await waitFor(() => expect(consumed).toEqual(["acc_limited"]));
  });
});
