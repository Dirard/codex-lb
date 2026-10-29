import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SHELL_STATUS_QUERY_KEY } from "@/components/layout/shell-status";
import { KeyPage } from "@/features/key-page/components/key-page";
import { localDateISO, shiftISODate } from "@/features/key-page/period";
import { KeyReportsPage } from "@/features/key-reports/key-reports-page";
import { createApiKey } from "@/test/mocks/factories";
import { createKeyPage } from "@/test/mocks/key-page-factories";
import { createShellStatus } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

type Recorded = { path: string; params: URLSearchParams };

function recordRequests(): Recorded[] {
  const requests: Recorded[] = [];
  server.events.on("request:start", ({ request }) => {
    const url = new URL(request.url);
    requests.push({ path: url.pathname, params: url.searchParams });
  });
  return requests;
}

function openAdmin(search = "", keyId = "key_1") {
  window.history.pushState({}, "", `/keys/${keyId}${search}`);
  return renderWithProviders(<KeyPage mode="admin" keyId={keyId} />);
}

function openOwner(search = "", onExit = vi.fn()) {
  window.history.pushState({}, "", `/${search}`);
  const view = renderWithProviders(<KeyReportsPage onExit={onExit} />);
  return { ...view, onExit };
}

const pageRequests = (requests: Recorded[], path = "/api/api-keys/key_1/page") => requests.filter((entry) => entry.path === path);

function precedes(a: Element, b: Element): boolean {
  return Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
}

afterEach(() => {
  server.events.removeAllListeners("request:start");
  vi.useRealTimers();
});

describe("KeyPage", () => {
  it("shows the sections in screen order with one period picker in the URL (R19, R24)", async () => {
    const user = userEvent.setup();
    const requests = recordRequests();
    openAdmin();

    expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();
    const headings = ["Limits", "Period totals", "Usage by day and model", "Errors by code", "Latency per day", "User agents",
      "Recent requests", "Key properties", "Cost by account", "Lifetime"].map((name) => screen.getByRole("heading", { name }));
    for (let index = 1; index < headings.length; index += 1) {
      expect(precedes(headings[index - 1], headings[index])).toBe(true);
    }
    expect(screen.getAllByRole("group", { name: "Period" })).toHaveLength(1);
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();

    const today = localDateISO();
    const first = pageRequests(requests)[0].params;
    expect(Object.fromEntries(first)).toMatchObject({ start_date: shiftISODate(today, -6), end_date: today });
    expect(first.get("timezone")).toBeTruthy();

    await user.click(within(screen.getByRole("group", { name: "Period" })).getByRole("button", { name: "30 d" }));
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("from")).toBe(shiftISODate(today, -29)));
    expect(new URLSearchParams(window.location.search).get("to")).toBe(today);
    await waitFor(() => expect(pageRequests(requests).at(-1)?.params.get("start_date")).toBe(shiftISODate(today, -29)));
    expect(screen.getByRole("button", { name: "30 d" })).toHaveAttribute("aria-pressed", "true");
  });

  it("applies a custom period from the URL and from the custom range", async () => {
    const user = userEvent.setup();
    const today = localDateISO();
    const from = shiftISODate(today, -40);
    const to = shiftISODate(today, -20);
    const requests = recordRequests();
    openAdmin(`?from=${from}&to=${to}`);

    await screen.findByRole("heading", { name: "Default key" });
    expect(Object.fromEntries(pageRequests(requests)[0].params)).toMatchObject({ start_date: from, end_date: to });
    expect(screen.getByRole("button", { name: "Custom" })).toHaveAttribute("aria-pressed", "true");
    const start = screen.getByLabelText("From");
    await user.clear(start);
    await user.type(start, shiftISODate(today, -30));
    await user.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("from")).toBe(shiftISODate(today, -30)));
  });

  it("polls every 60 s while the period includes today and writes the shell status", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const requests = recordRequests();
    server.use(
      http.get("/api/api-keys/:keyId/page", () =>
        HttpResponse.json(createKeyPage(createApiKey(), { overrides: { shell: createShellStatus({ availableResetCredits: 9 }) } })),
      ),
    );
    const { queryClient } = openAdmin();
    const writes = vi.spyOn(queryClient, "setQueryData");
    await screen.findByRole("heading", { name: "Default key" });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    await waitFor(() => expect(pageRequests(requests).length).toBeGreaterThanOrEqual(2));
    expect(requests.filter((entry) => entry.path === "/api/api-keys/key_1/requests")).toHaveLength(0);
    const shellWrite = writes.mock.calls.find(([key]) => JSON.stringify(key) === JSON.stringify(SHELL_STATUS_QUERY_KEY));
    expect((shellWrite?.[1] as { availableResetCredits: number } | undefined)?.availableResetCredits).toBe(9);
  });

  it("reads a historical period once and does not poll it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const today = localDateISO();
    const requests = recordRequests();
    openAdmin(`?from=${shiftISODate(today, -14)}&to=${shiftISODate(today, -1)}`);
    await screen.findByRole("heading", { name: "Default key" });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(130_000);
    });
    expect(pageRequests(requests)).toHaveLength(1);
  });

  it("shows the administrator's actions, account columns, cost by account and lifetime", async () => {
    openAdmin();

    expect(await screen.findByRole("heading", { name: "Cost by account" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Lifetime" })).toBeInTheDocument();
    for (const name of ["Edit", "Edit limits", "Disable", "Copy owner link", "More key actions"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    const requests = screen.getByRole("region", { name: "Recent requests" });
    for (const column of ["Account", "Client IP", "Conversation"]) {
      expect(within(requests).getByRole("columnheader", { name: column })).toBeInTheDocument();
    }
    expect(screen.getByRole("link", { name: "Keys" })).toHaveAttribute("href", "/keys");
  });

  it("opens the request details for the administrator", async () => {
    const user = userEvent.setup();
    openAdmin();

    await user.click(await screen.findByRole("button", { name: "Details of req_key_1_0003" }));
    const dialog = await screen.findByRole("dialog", { name: "Request Details" });
    expect(within(dialog).getByText("203.0.113.10")).toBeInTheDocument();
    expect(within(dialog).getByText("Rate limit exceeded")).toBeInTheDocument();
  });

  it("loads more requests with the cursor of the previous page, without polling them", async () => {
    const user = userEvent.setup();
    const requests = recordRequests();
    openAdmin();

    const section = await screen.findByRole("region", { name: "Recent requests" });
    expect(within(section).getAllByTestId("key-request-row")).toHaveLength(50);
    await user.click(within(section).getByRole("button", { name: "Load more" }));

    await waitFor(() => expect(within(section).getAllByTestId("key-request-row")).toHaveLength(60));
    const more = requests.filter((entry) => entry.path === "/api/api-keys/key_1/requests");
    expect(more).toHaveLength(1);
    expect(Object.fromEntries(more[0].params)).toEqual({ cursor: "50", limit: "50", errors_only: "false" });
    expect(within(section).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("reads errors only from the newest request when toggled", async () => {
    const user = userEvent.setup();
    const requests = recordRequests();
    openAdmin();

    const section = await screen.findByRole("region", { name: "Recent requests" });
    await user.click(within(section).getByRole("switch", { name: "Errors only" }));

    await waitFor(() => expect(within(section).getAllByTestId("key-request-row")).toHaveLength(6));
    for (const row of within(section).getAllByTestId("key-request-row")) {
      expect(row).toHaveTextContent("rate_limit_exceeded");
    }
    const calls = requests.filter((entry) => entry.path === "/api/api-keys/key_1/requests");
    expect(Object.fromEntries(calls[0].params)).toEqual({ limit: "50", errors_only: "true" });

    await user.click(within(section).getByRole("switch", { name: "Errors only" }));
    expect(within(section).getAllByTestId("key-request-row")).toHaveLength(50);
    await user.click(within(section).getByRole("switch", { name: "Errors only" }));
    await waitFor(() => expect(requests.filter((entry) => entry.path === "/api/api-keys/key_1/requests")).toHaveLength(2));
    expect(requests.filter((entry) => entry.path === "/api/api-keys/key_1/requests")[1].params.has("cursor")).toBe(false);
  });

  it("links the errors to the Requests page for the administrator", async () => {
    openAdmin();

    const errors = await screen.findByRole("region", { name: "Errors by code" });
    expect(within(errors).getByText("No code")).toBeInTheDocument();
    expect(within(errors).getByRole("link", { name: "Show these requests" })).toHaveAttribute(
      "href",
      "/requests?api_key_id=key_1&status=error&timeframe=7d",
    );
  });

  it("shows the header from the directory while the page loads", async () => {
    server.use(http.get("/api/api-keys/:keyId/page", () => new Promise<never>(() => {})));
    openAdmin();

    expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();
    expect(screen.getByTestId("key-page-skeleton")).toBeInTheDocument();
    expect(screen.queryByText("Loading...")).not.toBeInTheDocument();
  });

  it("keeps the previous data with a retryable error when a refetch fails", async () => {
    const user = userEvent.setup();
    let fail = false;
    server.use(
      http.get("/api/api-keys/:keyId/page", () =>
        fail
          ? HttpResponse.json({ error: { code: "internal_error", message: "Temporarily unavailable" } }, { status: 500 })
          : HttpResponse.json(createKeyPage(createApiKey())),
      ),
    );
    openAdmin();
    expect(await screen.findByRole("heading", { name: "Lifetime" })).toBeInTheDocument();

    fail = true;
    await user.click(screen.getByRole("button", { name: "Refresh key page" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/Showing data from .*Temporarily unavailable/);
    expect(within(alert).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Lifetime" })).toBeInTheDocument();
  });

  it("says the period is empty with the last use, no limits and the catching-up notes", async () => {
    const today = localDateISO();
    server.use(
      http.get("/api/api-keys/:keyId/page", () =>
        HttpResponse.json(
          createKeyPage(createApiKey({ limits: [] }), {
            period: { from: shiftISODate(today, -30), to: shiftISODate(today, -20) },
            overrides: { rollupsUsable: false, detailsFrom: "2026-09-01T00:00:00Z" },
          }),
        ),
      ),
    );
    openAdmin(`?from=${shiftISODate(today, -30)}&to=${shiftISODate(today, -20)}`);

    expect(await screen.findByText("No requests in this period")).toBeInTheDocument();
    expect(screen.getByText(/^Last used /)).toBeInTheDocument();
    expect(screen.getByText("No limits configured")).toBeInTheDocument();
    expect(screen.getByText(/usage analytics are catching up/i)).toBeInTheDocument();
    expect(screen.getByText(/Latency, user agents and error codes are not kept before/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Period totals" })).not.toBeInTheDocument();
  });

  it("says the key does not exist", async () => {
    openAdmin("", "key_missing");
    expect(await screen.findByText("This API key does not exist or was deleted.")).toBeInTheDocument();
  });

  it("disables after confirmation and deletes the key, returning to the key list", async () => {
    const user = userEvent.setup();
    const patches: unknown[] = [];
    server.use(
      http.patch("/api/api-keys/:keyId", async ({ request }) => {
        patches.push(await request.json());
        return HttpResponse.json(createApiKey({ isActive: false }));
      }),
    );
    openAdmin();

    await user.click(await screen.findByRole("button", { name: "Disable" }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Disable" }));
    await waitFor(() => expect(patches).toEqual([{ isActive: false }]));

    await user.click(screen.getByRole("button", { name: "More key actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    await user.click(within(await screen.findByRole("alertdialog", { name: "Delete API key" })).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(window.location.pathname).toBe("/keys"));
  });

  it("moves the key to a group of the directory", async () => {
    const user = userEvent.setup();
    const patches: unknown[] = [];
    server.use(
      http.get("/api/directory", () =>
        HttpResponse.json({ keys: [{ id: "key_1", name: "Default key", prefix: "sk-test", groupId: null, status: "active" }],
          groups: [{ id: "grp_a", name: "Team A", accountCount: 1, keyCount: 0 }], accounts: [] }),
      ),
      http.patch("/api/api-keys/:keyId", async ({ request }) => {
        patches.push(await request.json());
        return HttpResponse.json(createApiKey({ groupId: "grp_a" }));
      }),
    );
    openAdmin();

    await user.click(await screen.findByRole("button", { name: "More key actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Move to group" }));
    const dialog = await screen.findByRole("dialog", { name: "Move key to a group" });
    await user.click(within(dialog).getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "Team A" }));
    await user.click(within(dialog).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(patches).toEqual([{ groupId: "grp_a" }]));
  });
});

describe("KeyPage owner mode", () => {
  it("hides the administrator sections and requests only /api/key-reports/ (R19, R26)", async () => {
    const user = userEvent.setup();
    const requests = recordRequests();
    openOwner();

    expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Recent requests" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    for (const name of ["Edit", "Edit limits", "Disable", "Copy owner link", "More key actions"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole("heading", { name: "Cost by account" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Lifetime" })).not.toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    const section = screen.getByRole("region", { name: "Recent requests" });
    expect(within(section).queryByRole("columnheader", { name: "Account" })).not.toBeInTheDocument();
    expect(within(section).queryByRole("columnheader", { name: "Client IP" })).not.toBeInTheDocument();
    expect(within(section).queryByRole("button", { name: /Details of/ })).not.toBeInTheDocument();

    await user.click(within(section).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(section).getAllByTestId("key-request-row")).toHaveLength(60));
    await user.click(screen.getByRole("button", { name: "Show these requests" }));
    await waitFor(() => expect(within(section).getAllByTestId("key-request-row")).toHaveLength(6));

    expect(requests.length).toBeGreaterThan(0);
    for (const entry of requests) {
      expect(entry.path.startsWith("/api/key-reports/"), entry.path).toBe(true);
    }
    expect(requests.filter((entry) => entry.path === "/api/key-reports/requests").map((entry) => entry.params.get("errors_only")))
      .toEqual(["false", "true"]);
  });

  it("reads the owner period from the URL", async () => {
    const today = localDateISO();
    const requests = recordRequests();
    openOwner(`?from=${shiftISODate(today, -10)}&to=${shiftISODate(today, -3)}`);

    await screen.findByRole("heading", { name: "Default key" });
    expect(Object.fromEntries(pageRequests(requests, "/api/key-reports/page")[0].params)).toMatchObject({
      start_date: shiftISODate(today, -10),
      end_date: shiftISODate(today, -3),
    });
  });

  it("ends the session when the key is no longer valid", async () => {
    server.use(
      http.get("/api/key-reports/page", () =>
        HttpResponse.json({ error: { code: "invalid_api_key", message: "Invalid API key" } }, { status: 401 })),
    );
    const { onExit } = openOwner();
    await waitFor(() => expect(onExit).toHaveBeenCalledWith("The API key is invalid, disabled or expired."));
    expect(onExit).toHaveBeenCalledTimes(1);
  });

  it("keeps the session and the data on other failures", async () => {
    const user = userEvent.setup();
    let fail = false;
    server.use(
      http.get("/api/key-reports/page", () =>
        fail
          ? HttpResponse.json({ error: { code: "server_error", message: "Unavailable" } }, { status: 500 })
          : HttpResponse.json(createKeyPage(createApiKey(), { scope: "owner" })),
      ),
    );
    const { onExit } = openOwner();
    expect(await screen.findByRole("heading", { name: "Key properties" })).toBeInTheDocument();

    fail = true;
    await user.click(screen.getByRole("button", { name: "Refresh key page" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Unavailable");
    expect(screen.getByRole("heading", { name: "Key properties" })).toBeInTheDocument();
    expect(onExit).not.toHaveBeenCalled();
  });

  it("shows a skeleton instead of a loading text", async () => {
    server.use(http.get("/api/key-reports/page", () => new Promise<never>(() => {})));
    openOwner();
    expect(await screen.findByTestId("key-page-skeleton")).toBeInTheDocument();
    expect(screen.queryByText("Loading...")).not.toBeInTheDocument();
  });
});
