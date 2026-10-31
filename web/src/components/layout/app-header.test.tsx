import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppHeader } from "@/components/layout/app-header";
import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { server } from "@/test/mocks/server";
import { createShellStatus } from "@/test/mocks/overview-factories";

function renderHeader(initialEntry = "/settings", queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <AppHeader onLogout={vi.fn()} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function mockShell(overrides: Parameters<typeof createShellStatus>[0]) {
  server.use(http.get("/api/dashboard/status", () => HttpResponse.json(createShellStatus(overrides))));
}

function desktopNav() {
  return screen.getAllByRole("navigation")[0];
}

describe("AppHeader", () => {
  it("shows the available reset credits from the shell status, capped at 99+", async () => {
    mockShell({ availableResetCredits: 110 });

    renderHeader();

    expect(await within(desktopNav()).findByTestId("accounts-badge")).toHaveTextContent("99+");
  });

  it("shows accounts needing re-login before reset credits", async () => {
    mockShell({ reauthRequiredAccounts: 2, availableResetCredits: 8 });

    renderHeader();

    const badge = await within(desktopNav()).findByTestId("accounts-badge");
    expect(badge).toHaveTextContent("2");
    expect(badge).toHaveAttribute("title", "2 accounts need re-login");
  });

  it("hides the badge without re-login accounts and reset credits, or with reset badges disabled", async () => {
    mockShell({ availableResetCredits: 5, showResetCreditBadges: false });

    renderHeader();

    await screen.findAllByRole("link", { name: /Accounts/i });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(screen.queryByTestId("accounts-badge")).not.toBeInTheDocument();
  });

  it("lists the destinations in operator order with Automations in the bar", () => {
    renderHeader();

    const links = within(desktopNav()).getAllByRole("link").map((link) => link.textContent);
    expect(links).toEqual(["Overview", "Groups", "Accounts", "Keys", "Video", "Reports", "Automations", "Settings"]);
    expect(screen.queryByRole("button", { name: "Advanced" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Codex LB/ })).toHaveAttribute("href", "/overview");
  });

  it("makes no request of its own on the overview, whose poll carries the shell status (R1)", async () => {
    const requests: string[] = [];
    server.events.on("request:start", ({ request }) => {
      requests.push(new URL(request.url).pathname);
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(SHELL_STATUS_QUERY_KEY, createShellStatus({ availableResetCredits: 3 }));

    renderHeader("/overview", queryClient);

    expect(await within(desktopNav()).findByTestId("accounts-badge")).toHaveTextContent("3");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    server.events.removeAllListeners("request:start");
    expect(requests).toEqual([]);
  });

  it.each([
    ["/keys/key_1", false],
    ["/keys/key_1?from=2020-01-01&to=2020-01-07", true],
  ])("on the key page %s requests the status only for a historical period", async (entry, polled) => {
    const requests: string[] = [];
    server.events.on("request:start", ({ request }) => {
      requests.push(new URL(request.url).pathname);
    });

    renderHeader(entry);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    if (polled) await waitFor(() => expect(requests).toContain("/api/dashboard/status"));
    server.events.removeAllListeners("request:start");
    expect(requests.includes("/api/dashboard/status")).toBe(polled);
  });

  it("polls only the status endpoint on other pages, never the accounts or the readiness probe", async () => {
    const requests: string[] = [];
    server.events.on("request:start", ({ request }) => {
      requests.push(new URL(request.url).pathname);
    });

    renderHeader("/automations");

    await waitFor(() => expect(requests).toContain("/api/dashboard/status"));
    server.events.removeAllListeners("request:start");
    expect(requests).not.toContain("/api/accounts");
    expect(requests).not.toContain("/health/ready");
    expect(requests).not.toContain("/api/settings");
  });

  it("offers the global search", () => {
    renderHeader();

    expect(screen.getAllByRole("button", { name: "Search keys, groups and accounts" }).length).toBeGreaterThan(0);
  });
});
