import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { AlertsPanel } from "@/features/overview/components/alerts-panel";
import { useOverviewActions } from "@/features/overview/components/overview-actions";
import type { Alert } from "@/features/overview/schemas";
import { createAccountGroup } from "@/test/mocks/factories";
import { server } from "@/test/mocks/server";

function alert(overrides: Partial<Alert>): Alert {
  return {
    id: "rule:key:key_1:",
    rule: "key_limit",
    severity: "warning",
    subject: { type: "key", id: "key_1", name: "Default key" },
    window: null,
    value: 92,
    at: null,
    count: 1,
    action: { kind: "edit_limits", target: "key_1" },
    ...overrides,
  };
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{`${location.pathname}${location.search}`}</div>;
}

function Harness({ alerts }: { alerts: Alert[] }) {
  const { perform, dialogs } = useOverviewActions();
  return (
    <>
      <AlertsPanel alerts={alerts} onAction={perform} />
      {dialogs}
      <LocationProbe />
    </>
  );
}

function renderPanel(alerts: Alert[]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/overview"]}>
        <Routes>
          <Route path="*" element={<Harness alerts={alerts} />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("AlertsPanel", () => {
  it("shows a single line without alerts", () => {
    renderPanel([]);
    expect(screen.getByText("No alerts")).toBeInTheDocument();
  });

  it("shows six alerts and the rest behind show all", async () => {
    const user = userEvent.setup();
    renderPanel(Array.from({ length: 8 }, (_, i) => alert({ id: `a${i}`, subject: { type: "key", id: `k${i}`, name: `Key ${i}` } })));
    expect(screen.getAllByTestId("overview-alert")).toHaveLength(6);
    await user.click(screen.getByRole("button", { name: "Show all (8)" }));
    expect(screen.getAllByTestId("overview-alert")).toHaveLength(8);
  });

  it.each([
    [{ kind: "open_group", target: "grp_1" }, "/groups?selected=grp_1"],
    [{ kind: "open_accounts", target: "/accounts" }, "/accounts"],
    [{ kind: "open_requests", target: "/requests?status=error&timeframe=1h" }, "/requests?status=error&timeframe=1h"],
    [{ kind: "open_key_errors", target: "key_1" }, "/requests?api_key_id=key_1&status=error&timeframe=1h"],
    [{ kind: "open_list", target: "/keys?near_limit=1" }, "/keys?near_limit=1"],
  ] as const)("navigates for %o (R15)", async (action, route) => {
    const user = userEvent.setup();
    renderPanel([alert({ action })]);
    await user.click(screen.getByRole("button", { name: /Open group|Open accounts|Show requests|Show errors|Show all/ }));
    expect(screen.getByTestId("location")).toHaveTextContent(route);
  });

  it("opens the key editor through GET /api/api-keys/{id}", async () => {
    const user = userEvent.setup();
    const requested: string[] = [];
    server.events.on("request:start", ({ request }) => requested.push(new URL(request.url).pathname));
    renderPanel([alert({ action: { kind: "edit_limits", target: "key_1" } })]);
    expect(requested).toEqual([]);
    await user.click(screen.getByRole("button", { name: "Edit limits" }));
    expect(await screen.findByRole("dialog", { name: "Edit API key" })).toBeInTheDocument();
    server.events.removeAllListeners("request:start");
    expect(requested).toContain("/api/api-keys/key_1");
  });

  it("opens the group editor for a grouped key's limits", async () => {
    const user = userEvent.setup();
    server.use(http.get("/api/account-groups/", () => HttpResponse.json([createAccountGroup({ id: "grp_1", name: "Platform" })])));
    renderPanel([alert({ action: { kind: "open_group_limits", target: "grp_1" } })]);
    await user.click(screen.getByRole("button", { name: "Group limits" }));
    expect(await screen.findByRole("dialog", { name: "Edit account group" })).toBeInTheDocument();
  });

  it("confirms extending an expiring key", async () => {
    const user = userEvent.setup();
    renderPanel([
      alert({ rule: "key_expiry", at: new Date(Date.now() + 3_600_000).toISOString(), value: null, action: { kind: "extend_expiry", target: "key_1" } }),
    ]);
    await user.click(screen.getByRole("button", { name: "Extend 30 days" }));
    expect(await screen.findByRole("alertdialog", { name: "Extend Default key by 30 days?" })).toBeInTheDocument();
  });

  it("opens the OAuth re-login for an account", async () => {
    const user = userEvent.setup();
    renderPanel([
      alert({ rule: "account_reauth", severity: "critical", subject: { type: "account", id: "acc_1", name: "Main" }, value: null,
        action: { kind: "reauth", target: "acc_1" } }),
    ]);
    await user.click(screen.getByRole("button", { name: "Re-login" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });
});
