import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";

import App from "@/App";
import { createAccountSummary } from "@/test/mocks/factories";
import { createShellStatus } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

afterEach(() => {
  server.events.removeAllListeners("request:start");
  window.history.pushState({}, "", "/");
});

function trackRequests(): string[] {
  const requests: string[] = [];
  server.events.on("request:start", ({ request }) => {
    const url = new URL(request.url);
    requests.push(`${request.method} ${url.pathname}`);
  });
  return requests;
}

function row(accountId: string): HTMLElement {
  const match = screen.getAllByTestId("accounts-table-row").find((element) => element.getAttribute("data-account-id") === accountId);
  if (!match) throw new Error(`no row for ${accountId}`);
  return match;
}

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 50));
  });
}

describe("accounts flow integration", () => {
  it("tells accounts with the same e-mail apart by their account id", async () => {
    // Go sends no duplicate marker; the dashboard derives it from the list.
    const twins = ["acc-twin-0001-aaaaaaaaaaaa", "acc-twin-0002-bbbbbbbbbbbb"].map((accountId) =>
      createAccountSummary({ accountId, email: "twin@example.com", displayName: "twin@example.com" }),
    );
    const solo = createAccountSummary({ accountId: "acc-solo", email: "solo@example.com", displayName: "solo@example.com" });
    server.use(http.get("/api/accounts", () => HttpResponse.json({ accounts: [...twins, solo] })));

    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    for (const twin of twins) {
      expect((await screen.findAllByTitle(`Account ID ${twin.accountId}`)).length).toBeGreaterThan(0);
    }
    expect(screen.queryByTitle("Account ID acc-solo")).not.toBeInTheDocument();
  });

  it("calls no provider on load or selection and only on Check now (R42)", async () => {
    const user = userEvent.setup({ delay: null });
    const requests = trackRequests();
    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "Accounts" })).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "primary@example.com" }));
    let sheet = await screen.findByTestId("account-detail-sheet");
    expect(await within(sheet).findByText("Token Status")).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByTestId("account-detail-sheet")).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "secondary@example.com" }));
    sheet = await screen.findByTestId("account-detail-sheet");
    await settle();

    expect(requests.filter((request) => request.includes("usage-reset-credits"))).toEqual([]);
    expect(requests).toContain("GET /api/accounts/acc_secondary/trends");
    // The page poll carries the shell status: no own status or readiness poll.
    for (const path of ["GET /api/dashboard/status", "GET /health/ready"]) {
      expect(requests).not.toContain(path);
    }

    // The paused account cannot be checked; an active one can.
    expect(within(sheet).getByRole("button", { name: "Check now" })).toBeDisabled();
    await user.click(within(sheet).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByTestId("account-detail-sheet")).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "primary@example.com" }));
    sheet = await screen.findByTestId("account-detail-sheet");
    await user.click(within(sheet).getByRole("button", { name: "Check now" }));

    await waitFor(() => expect(within(sheet).getByTestId("reset-credits-value")).toHaveTextContent(/^3 available as of \d\d:\d\d$/));
    expect(requests.filter((request) => request.includes("usage-reset-credits"))).toEqual([
      "GET /api/accounts/acc_primary/usage-reset-credits",
    ]);
  });

  it("labels every usage figure with its period (R44)", async () => {
    const user = userEvent.setup({ delay: null });
    window.history.pushState({}, "", "/accounts?selected=acc_primary");
    renderWithProviders(<App />);

    const sheet = await screen.findByTestId("account-detail-sheet");
    expect(within(sheet).getByTestId("usage-last-7-days")).toHaveTextContent("Last 7 days");
    expect(within(sheet).getByTestId("usage-last-7-days")).toHaveTextContent("36K");
    expect(within(sheet).getByTestId("usage-lifetime")).toHaveTextContent("Lifetime");
    expect(within(sheet).getByTestId("usage-lifetime")).toHaveTextContent("128K");
    expect(within(sheet).getByText("5h remaining")).toBeInTheDocument();
    expect(within(sheet).getByText("Weekly remaining")).toBeInTheDocument();
    expect(within(sheet).queryByText("Request logs total")).not.toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Close" }));

    // Quota cells name their window (column) and carry the reset time.
    const table = screen.getByTestId("accounts-table");
    expect(within(table).getAllByText(/^Resets /).length).toBeGreaterThan(0);
  });

  it("changes priority, pauses, resumes and re-logs in from the row without a reload (R45)", async () => {
    const user = userEvent.setup({ delay: null });
    const requests = trackRequests();
    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    await screen.findByRole("button", { name: "primary@example.com" });
    await user.click(within(row("acc_primary")).getByRole("combobox", { name: "Priority of primary@example.com" }));
    await user.click(await screen.findByRole("option", { name: "Preserve" }));
    await waitFor(() =>
      expect(within(row("acc_primary")).getByRole("combobox", { name: "Priority of primary@example.com" })).toHaveTextContent("Preserve"),
    );
    expect(requests).toContain("PUT /api/accounts/acc_primary/routing-policy");

    await user.click(within(row("acc_primary")).getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(within(row("acc_primary")).getByRole("button", { name: "Resume" })).toBeInTheDocument());
    expect(within(row("acc_primary")).getByText("Paused")).toBeInTheDocument();
    await user.click(within(row("acc_primary")).getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(within(row("acc_primary")).getByRole("button", { name: "Pause" })).toBeInTheDocument());

    await user.click(within(row("acc_primary")).getByRole("button", { name: "More actions for primary@example.com" }));
    await user.click(await screen.findByRole("menuitem", { name: "Re-login" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    // The page never reloaded: the same document still holds the table.
    expect(screen.getByTestId("accounts-table")).toBeInTheDocument();
  });

  it("re-logs in an account needing it inline", async () => {
    const user = userEvent.setup({ delay: null });
    server.use(
      http.get("/api/accounts", () =>
        HttpResponse.json({
          accounts: [createAccountSummary({ accountId: "acc_reauth", email: "reauth@example.com", displayName: "reauth@example.com", status: "reauth_required" })],
          shell: createShellStatus({ reauthRequiredAccounts: 1 }),
        }),
      ),
    );
    window.history.pushState({}, "", "/accounts");
    renderWithProviders(<App />);

    await user.click(await within(await screen.findByTestId("accounts-table")).findByRole("button", { name: "Re-login" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("lets operators set, search, and clear an account alias", async () => {
    const user = userEvent.setup({ delay: null });

    window.history.pushState({}, "", "/accounts?selected=acc_primary");
    renderWithProviders(<App />);

    const sheet = await screen.findByTestId("account-detail-sheet");
    await user.click(within(sheet).getByRole("button", { name: "Edit alias" }));
    const aliasInput = await within(sheet).findByLabelText("Account alias");
    await user.clear(aliasInput);
    await user.type(aliasInput, "Personal Plus");
    await user.click(within(sheet).getByRole("button", { name: "Save alias" }));

    await waitFor(() => {
      expect(within(sheet).getByRole("heading", { name: "Personal Plus" })).toBeInTheDocument();
    });
    expect(within(row("acc_primary")).getByText("Personal Plus")).toBeInTheDocument();

    await user.click(within(sheet).getByRole("button", { name: "Edit alias" }));
    const aliasInputToClear = await within(sheet).findByLabelText("Account alias");
    await user.clear(aliasInputToClear);
    await user.click(within(sheet).getByRole("button", { name: "Save alias" }));
    await waitFor(() => {
      expect(within(sheet).getByRole("heading", { name: "primary@example.com" })).toBeInTheDocument();
    });
    await user.click(within(sheet).getByRole("button", { name: "Close" }));

    await user.type(screen.getByRole("textbox", { name: "Search alias, e-mail or id" }), "secondary");
    expect(screen.getAllByTestId("accounts-table-row")).toHaveLength(1);
  });
});
