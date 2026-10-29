import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "@/App";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

function recordRequests() {
  const logs: URLSearchParams[] = [];
  const options: URLSearchParams[] = [];
  const conversations: URLSearchParams[] = [];
  server.use(
    http.get("/api/request-logs", ({ request }) => {
      logs.push(new URL(request.url).searchParams);
      return HttpResponse.json({ requests: [], total: 0, hasMore: false });
    }),
    http.get("/api/request-logs/options", ({ request }) => {
      options.push(new URL(request.url).searchParams);
      return HttpResponse.json({ accountIds: [], apiKeys: [], modelOptions: [], statuses: ["ok", "error"] });
    }),
    http.get("/api/conversations", ({ request }) => {
      conversations.push(new URL(request.url).searchParams);
      return HttpResponse.json({ conversations: [], total: 0, hasMore: false });
    }),
  );
  return { logs, options, conversations };
}

afterEach(() => {
  vi.useRealTimers();
  window.history.pushState({}, "", "/");
});

describe("requests page flow", () => {
  it("defaults to 24 hours with one period picker and does not poll until Live is on (R18)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = recordRequests();
    window.history.pushState({}, "", "/requests");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "Requests" })).toBeInTheDocument();
    await waitFor(() => expect(calls.logs).toHaveLength(1));
    expect(calls.logs[0].get("timeframe")).toBe("24h");
    expect(screen.getAllByRole("combobox")).toHaveLength(1);
    expect(screen.getByRole("combobox", { name: "Period" })).toHaveTextContent("Last 24 hours");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(65_000);
    });
    expect(calls.logs).toHaveLength(1);
    expect(calls.options).toHaveLength(0);

    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await user.click(screen.getByRole("switch", { name: "Live" }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    await waitFor(() => expect(calls.logs.length).toBeGreaterThanOrEqual(2));
  });

  it("fetches filter options for the selected timeframe only when a filter opens", async () => {
    const user = userEvent.setup();
    const calls = recordRequests();
    window.history.pushState({}, "", "/requests?timeframe=7d");
    renderWithProviders(<App />);

    await waitFor(() => expect(calls.logs).toHaveLength(1));
    expect(calls.options).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Statuses" }));
    await waitFor(() => expect(calls.options).toHaveLength(1));
    expect(calls.options[0].get("timeframe")).toBe("7d");
  });

  it("prefills the error status and last hour from an alert link", async () => {
    const calls = recordRequests();
    window.history.pushState({}, "", "/requests?status=error&timeframe=1h");
    renderWithProviders(<App />);

    await waitFor(() => expect(calls.logs).toHaveLength(1));
    expect(calls.logs[0].getAll("status")).toEqual(["error"]);
    expect(calls.logs[0].get("timeframe")).toBe("1h");
    expect(await screen.findByRole("combobox", { name: "Period" })).toHaveTextContent("Last hour");
  });

  it("shares the period with the conversation view", async () => {
    const user = userEvent.setup();
    const calls = recordRequests();
    window.history.pushState({}, "", "/requests?timeframe=30d");
    renderWithProviders(<App />);

    await waitFor(() => expect(calls.logs).toHaveLength(1));
    expect(calls.logs[0].get("timeframe")).toBe("30d");
    await user.click(screen.getByRole("button", { name: "Request Logs" }));
    await user.click(await screen.findByRole("menuitemradio", { name: "Conversations" }));

    await waitFor(() => expect(calls.conversations).toHaveLength(1));
    expect(calls.conversations[0].get("timeframe")).toBe("30d");
    expect(calls.conversations[0].has("since")).toBe(false);
    expect(window.location.search).toContain("view=conversations");
  });
});
