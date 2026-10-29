import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { OverviewPage } from "@/features/overview/components/overview-page";
import type { Alert } from "@/features/overview/schemas";
import { createAttentionKey, createGroupRow, createOverview, createShellStatus } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const ALERT: Alert = {
  id: "account_reauth:account:acc_1:",
  rule: "account_reauth",
  severity: "critical",
  subject: { type: "account", id: "acc_1", name: "Main" },
  window: null,
  value: null,
  at: null,
  count: 1,
  action: { kind: "reauth", target: "acc_1" },
};

function precedes(a: Element, b: Element): boolean {
  return Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
}

afterEach(() => {
  vi.useRealTimers();
});

describe("OverviewPage", () => {
  it("renders alerts, pool, groups and attention keys in that order without a period picker (R7)", async () => {
    server.use(
      http.get("/api/overview", () =>
        HttpResponse.json(createOverview({ alerts: [ALERT], attentionKeys: [createAttentionKey()], attentionKeysTotal: 1 })),
      ),
    );
    renderWithProviders(<OverviewPage />);

    const alerts = await screen.findByRole("heading", { name: /Alerts/ });
    const pool = screen.getByRole("heading", { name: "Pool" });
    const groups = screen.getByRole("heading", { name: "Groups" });
    const attention = screen.getByRole("heading", { name: "Keys needing attention" });
    expect(precedes(alerts, pool) && precedes(pool, groups) && precedes(groups, attention)).toBe(true);
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    for (const label of ["Spend today", "Requests today", "Errors, last hour", "Spend, 7 days", "5 h remaining", "Weekly remaining"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(screen.getByText("Main needs re-login")).toBeInTheDocument();
    expect(screen.getAllByText("Ungrouped keys").length).toBeGreaterThan(0);
  });

  it("polls only the overview, every 30 s, and writes the shell status (R1, R8)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const requests: string[] = [];
    server.events.on("request:start", ({ request }) => requests.push(new URL(request.url).pathname));
    server.use(http.get("/api/overview", () => HttpResponse.json(createOverview({ shell: createShellStatus({ availableResetCredits: 7 }) }))));
    const { queryClient } = renderWithProviders(<OverviewPage />);
    const writes = vi.spyOn(queryClient, "setQueryData");

    await screen.findByRole("heading", { name: "Pool" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    await waitFor(() => expect(requests.filter((path) => path === "/api/overview").length).toBeGreaterThanOrEqual(3));
    server.events.removeAllListeners("request:start");
    expect(new Set(requests)).toEqual(new Set(["/api/overview"]));
    const shellWrite = writes.mock.calls.find(([key]) => JSON.stringify(key) === JSON.stringify(SHELL_STATUS_QUERY_KEY));
    expect((shellWrite?.[1] as { availableResetCredits: number } | undefined)?.availableResetCredits).toBe(7);
  });

  it("keeps the previous data with a stale label when a refetch fails", async () => {
    const user = userEvent.setup();
    let fail = false;
    server.use(
      http.get("/api/overview", () =>
        fail
          ? HttpResponse.json({ error: { code: "internal_error", message: "The operation could not be completed" } }, { status: 500 })
          : HttpResponse.json(createOverview({ groups: [createGroupRow({ name: "Kept group" })] })),
      ),
    );
    renderWithProviders(<OverviewPage />);
    expect(await screen.findByText("Kept group")).toBeInTheDocument();

    fail = true;
    await user.click(screen.getByRole("button", { name: "Refresh overview" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/Showing data from/);
    expect(within(alert.parentElement as HTMLElement).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByText("Kept group")).toBeInTheDocument();
  });

  it("hides trends and says analytics are catching up while rollups are unusable (R9)", async () => {
    server.use(
      http.get("/api/overview", () => {
        const overview = createOverview({ shell: createShellStatus({ rollupsUsable: false }) });
        overview.pool.trend7d = null;
        overview.pool.spend7dUsd = null;
        overview.pool.spendYesterdaySameTimeUsd = null;
        return HttpResponse.json(overview);
      }),
    );
    renderWithProviders(<OverviewPage />);

    expect(await screen.findByText("Usage analytics are catching up; 7-day trends are hidden.")).toBeInTheDocument();
    expect(screen.queryByTestId("sparkline")).not.toBeInTheDocument();
    expect(screen.getByText("yesterday unknown")).toBeInTheDocument();
  });

  it("shows empty states and keeps an expanded group row across polls", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    server.use(
      http.get("/api/overview", () =>
        HttpResponse.json(
          createOverview({
            groups: [
              createGroupRow({
                problemAccounts: [{ id: "acc_p", label: "Paused one", status: "paused", health: "paused",
                  primaryRemainingPercent: 40, primaryResetAt: null, secondaryRemainingPercent: 70, secondaryResetAt: null, action: "resume" }],
                topKeys: [{ id: "key_t", name: "Top key", spendTodayUsd: 2, spend7dUsd: 9, maxLimitPercent: 50, errorRateLastHour: 0, lastUsedAt: null }],
              }),
            ],
          }),
        ),
      ),
    );
    renderWithProviders(<OverviewPage />);

    expect(await screen.findByText("No alerts")).toBeInTheDocument();
    expect(screen.getByText("No keys need attention")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Expand Team A" }));
    expect(screen.getByText("Paused one")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Top key" })).toHaveAttribute("href", "/keys/key_t");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(screen.getByText("Paused one")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Collapse Team A" })).toHaveAttribute("aria-expanded", "true");
  });
});
