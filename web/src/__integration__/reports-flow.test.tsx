import { focusManager } from "@tanstack/react-query";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "@/App";
import { addDaysISO, daysAgoLocalISO, localDateISO } from "@/features/reports/date";
import type { Directory } from "@/features/search/api";
import {
  buildKeyUsageReport,
  createKeyReportRow,
  createReportsResponse,
  createUsageMeasures,
} from "@/test/mocks/key-usage-report-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";
import { copyToClipboard } from "@/utils/clipboard";

vi.mock("@/utils/clipboard", () => ({ copyToClipboard: vi.fn().mockResolvedValue(true) }));

const DIRECTORY: Directory = {
  groups: [
    { id: "grp_a", name: "Team A", accountCount: 1, keyCount: 1 },
    { id: "grp_b", name: "Team B", accountCount: 1, keyCount: 1 },
    { id: "grp_c", name: "Team C", accountCount: 0, keyCount: 0 },
  ],
  keys: [
    { id: "key_a", name: "Alpha", prefix: "sk-a", groupId: "grp_a", status: "active" },
    { id: "key_b", name: "Beta", prefix: "sk-b", groupId: "grp_b", status: "active" },
  ],
  accounts: [],
};

const ROWS = [
  createKeyReportRow({ id: "key_a", name: "Alpha", prefix: "sk-a", groupId: "grp_a", groupName: "Team A", measures: createUsageMeasures({ requests: 12, costUsd: 3 }) }),
  createKeyReportRow({ id: "key_b", name: "Beta", prefix: "sk-b", groupId: "grp_b", groupName: "Team B", measures: createUsageMeasures({ requests: 4, costUsd: 1 }) }),
];

function recordReports() {
  const paths: string[] = [];
  const keys: URLSearchParams[] = [];
  const traffic: URLSearchParams[] = [];
  server.events.on("request:start", ({ request }) => paths.push(new URL(request.url).pathname));
  server.use(
    http.get("/api/directory", () => HttpResponse.json(DIRECTORY)),
    http.get("/api/reports/keys", ({ request }) => {
      const params = new URL(request.url).searchParams;
      keys.push(params);
      const groups = params.getAll("group_id");
      const selected = groups.length === 0 ? ROWS : ROWS.filter((row) => row.groupId !== null && groups.includes(row.groupId));
      return HttpResponse.json(
        buildKeyUsageReport({
          start: params.get("start_date") ?? "",
          end: params.get("end_date") ?? "",
          keys: selected,
          selection: { groupIds: groups, allKeys: groups.length === 0, resolvedKeyCount: selected.length },
        }),
      );
    }),
    http.get("/api/reports", ({ request }) => {
      traffic.push(new URL(request.url).searchParams);
      return HttpResponse.json(createReportsResponse());
    }),
  );
  return { paths, keys, traffic };
}

async function settle(ms = 50) {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
}

afterEach(() => {
  vi.useRealTimers();
  server.events.removeAllListeners("request:start");
  vi.mocked(copyToClipboard).mockClear();
  window.history.pushState({}, "", "/");
});

describe("reports flow (R32)", () => {
  it("restores a copied link with one report request and round-trips selection and period through the URL", async () => {
    const user = userEvent.setup();
    const calls = recordReports();
    window.history.pushState({}, "", "/reports?group=grp_a&group=grp_b&period=30d");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { level: 1, name: "Reports" })).toBeInTheDocument();
    expect(await screen.findByTestId("key-report-summary")).toBeInTheDocument();
    await settle();
    expect(calls.keys).toHaveLength(1);
    expect(calls.keys[0].getAll("group_id")).toEqual(["grp_a", "grp_b"]);
    expect(calls.keys[0].get("start_date")).toBe(daysAgoLocalISO(29));
    expect(calls.keys[0].get("end_date")).toBe(localDateISO());
    expect(screen.getByRole("button", { name: "30d" })).toHaveAttribute("aria-pressed", "true");
    await waitFor(() =>
      expect(screen.getAllByTestId("scope-chip").map((chip) => chip.textContent)).toEqual(["Team A1 key", "Team B1 key"]),
    );

    await user.click(screen.getByRole("button", { name: "Copy link" }));
    expect(copyToClipboard).toHaveBeenCalledWith(`${window.location.origin}/reports?group=grp_a&group=grp_b&period=30d`);

    await user.click(screen.getByRole("button", { name: "Remove Team B" }));
    await user.click(screen.getByRole("button", { name: "Last month" }));
    const search = new URLSearchParams(window.location.search);
    expect(search.getAll("group")).toEqual(["grp_a"]);
    expect(search.get("period")).toBe("last_month");
    await waitFor(() => expect(calls.keys.at(-1)?.getAll("group_id")).toEqual(["grp_a"]));
    const now = new Date();
    expect(calls.keys.at(-1)?.get("start_date")).toBe(localDateISO(new Date(now.getFullYear(), now.getMonth() - 1, 1)));

    fireEvent.change(screen.getByLabelText("End date"), { target: { value: addDaysISO(localDateISO(), -2) } });
    const custom = new URLSearchParams(window.location.search);
    expect(custom.has("period")).toBe(false);
    expect(custom.get("to")).toBe(addDaysISO(localDateISO(), -2));
    expect(screen.getByRole("button", { name: "Last month" })).toHaveAttribute("aria-pressed", "false");
    for (const path of ["/api/accounts", "/api/request-logs/options", "/api/reports"]) {
      expect(calls.paths).not.toContain(path);
    }
  });

  it("copies an owner link of the report period without a credential", async () => {
    const user = userEvent.setup();
    recordReports();
    window.history.pushState({}, "", `/reports?from=${daysAgoLocalISO(13)}&to=${daysAgoLocalISO(1)}`);
    renderWithProviders(<App />);

    const row = (await screen.findAllByTestId("per-key-row"))[0];
    expect(within(row).getByRole("link", { name: "Open" })).toHaveAttribute("href", "/keys/key_a");
    await user.click(within(row).getByRole("button", { name: "Copy owner link" }));
    expect(copyToClipboard).toHaveBeenCalledWith(`${window.location.origin}/?from=${daysAgoLocalISO(13)}&to=${daysAgoLocalISO(1)}`);
  });

  it("requests traffic details only once they are expanded", async () => {
    const user = userEvent.setup();
    const calls = recordReports();
    window.history.pushState({}, "", "/reports?group=grp_a");
    renderWithProviders(<App />);
    await screen.findByTestId("key-report-summary");
    await settle();
    expect(calls.traffic).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: /Traffic details/ }));
    await waitFor(() => expect(calls.traffic).toHaveLength(1));
    expect(calls.traffic[0].getAll("api_key_id")).toEqual(["key_a"]);
    expect(await screen.findByText("Distribution by Model")).toBeInTheDocument();
  });

  it("does not poll: no second report request after minutes without focus", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = recordReports();
    window.history.pushState({}, "", "/reports");
    renderWithProviders(<App />);
    await screen.findByTestId("key-report-summary");
    expect(calls.keys).toHaveLength(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(calls.keys).toHaveLength(1);
    expect(calls.traffic).toHaveLength(0);
    for (const path of ["/api/accounts", "/api/request-logs/options"]) {
      expect(calls.paths).not.toContain(path);
    }

    // Regaining focus more than 60 s after the response refetches a period including today.
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => expect(calls.keys).toHaveLength(2));
    act(() => {
      focusManager.setFocused(undefined);
    });
  });

  it("does not refetch a past period on focus", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = recordReports();
    window.history.pushState({}, "", "/reports?period=last_month");
    renderWithProviders(<App />);
    await screen.findByTestId("key-report-summary");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2 * 60_000);
    });
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await settle();
    expect(calls.keys).toHaveLength(1);
    act(() => {
      focusManager.setFocused(undefined);
    });
  });
});
