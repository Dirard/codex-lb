import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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

import { addDaysISO, daysAgoLocalISO, getBrowserReportsTimeZone, localDateISO } from "../date";
import type { KeyReportRow } from "../schemas";
import { ReportsPage } from "./reports-page";

vi.mock("@/utils/clipboard", () => ({ copyToClipboard: vi.fn().mockResolvedValue(true) }));

const DIRECTORY: Directory = {
  groups: [
    { id: "grp_a", name: "Team A", accountCount: 1, keyCount: 1 },
    { id: "grp_b", name: "Team B", accountCount: 1, keyCount: 1 },
  ],
  keys: [
    { id: "key_a", name: "Alpha", prefix: "sk-a", groupId: "grp_a", status: "active" },
    { id: "key_b", name: "Beta", prefix: "sk-b", groupId: "grp_b", status: "active" },
    { id: "key_u", name: "Loose", prefix: "sk-u", groupId: null, status: "active" },
  ],
  accounts: [],
};

function row(id: string, name: string, groupId: string | null, requests: number, costUsd: number): KeyReportRow {
  return createKeyReportRow({
    id,
    name,
    prefix: `sk-${id}`,
    groupId,
    groupName: groupId ? DIRECTORY.groups.find((group) => group.id === groupId)?.name ?? null : null,
    measures: createUsageMeasures({ requests, inputTokens: requests * 10, outputTokens: requests, costUsd }),
  });
}

const ROWS = [row("key_a", "Alpha", "grp_a", 10, 2), row("key_b", "Beta", "grp_b", 5, 1), row("key_u", "Loose", null, 0, 0)];

type Recorded = { keys: URLSearchParams[]; traffic: URLSearchParams[]; directory: number; paths: string[] };

function mockApi(options: { keys?: (params: URLSearchParams) => KeyReportRow[]; keysError?: { status: number; code: string; message: string }; directoryError?: boolean } = {}): Recorded {
  const recorded: Recorded = { keys: [], traffic: [], directory: 0, paths: [] };
  server.events.on("request:start", ({ request }) => recorded.paths.push(new URL(request.url).pathname));
  server.use(
    http.get("/api/directory", () => {
      recorded.directory += 1;
      return options.directoryError
        ? HttpResponse.json({ error: { code: "unavailable", message: "Directory unavailable" } }, { status: 503 })
        : HttpResponse.json(DIRECTORY);
    }),
    http.get("/api/reports/keys", ({ request }) => {
      const params = new URL(request.url).searchParams;
      recorded.keys.push(params);
      if (options.keysError) {
        return HttpResponse.json({ error: { code: options.keysError.code, message: options.keysError.message } }, { status: options.keysError.status });
      }
      const keys = options.keys ? options.keys(params) : ROWS;
      return HttpResponse.json(
        buildKeyUsageReport({
          start: params.get("start_date") ?? "",
          end: params.get("end_date") ?? "",
          keys,
          selection: {
            groupIds: params.getAll("group_id"),
            keyIds: params.getAll("api_key_id"),
            ungrouped: params.get("ungrouped") === "1",
            allKeys: !params.has("group_id") && !params.has("api_key_id") && !params.has("ungrouped"),
            resolvedKeyCount: keys.length,
          },
          previous: createUsageMeasures({ requests: 10, costUsd: 1.5 }),
        }),
      );
    }),
    http.get("/api/reports", ({ request }) => {
      recorded.traffic.push(new URL(request.url).searchParams);
      return HttpResponse.json(createReportsResponse());
    }),
  );
  return recorded;
}

function renderPage(path = "/reports") {
  window.history.pushState({}, "", path);
  return renderWithProviders(<ReportsPage />);
}

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 30));
  });
}

beforeEach(() => {
  vi.mocked(copyToClipboard).mockClear();
});

afterEach(() => {
  server.events.removeAllListeners("request:start");
});

describe("ReportsPage", () => {
  it("renders the selection bar, the report sections and the English labels from one report request", async () => {
    const calls = mockApi();
    renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "Reports" })).toBeInTheDocument();
    expect(screen.getByText("Usage by API key and group")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Keys and groups/ })).toHaveTextContent("All keys");
    for (const label of ["Today", "Yesterday", "7d", "30d", "90d", "This month", "Last month"]) {
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    }
    expect(screen.getByRole("button", { name: "7d" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText("Start date")).toHaveValue(daysAgoLocalISO(6));
    expect(screen.getByLabelText("End date")).toHaveValue(localDateISO());
    expect(screen.getByText(`Time zone: ${getBrowserReportsTimeZone() ?? "UTC"}`)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    expect(screen.getByTestId("reports-skeleton")).toBeInTheDocument();

    expect(await screen.findByTestId("key-report-summary")).toBeInTheDocument();
    for (const label of ["Requests", "Tokens", "Cost", "Errors", "Active keys"]) {
      expect(within(screen.getByTestId("key-report-summary")).getByText(label)).toBeInTheDocument();
    }
    for (const heading of ["Groups", "Keys", "Daily usage", "Traffic details"]) {
      expect(screen.getByRole("heading", { name: new RegExp(`^${heading}`) })).toBeInTheDocument();
    }
    expect(screen.getAllByTestId("group-report-row").map((element) => within(element).getAllByRole("cell")[0].textContent)).toEqual([
      "Team A",
      "Team B",
      "Ungrouped keys",
    ]);
    expect(screen.getAllByRole("button", { name: "Copy owner link" })).toHaveLength(2);

    const exportLink = screen.getByRole("link", { name: "Export CSV" });
    const href = new URL(exportLink.getAttribute("href") ?? "", "http://localhost");
    expect(href.pathname).toBe("/api/reports/keys/export.csv");
    expect(href.searchParams.get("start_date")).toBe(daysAgoLocalISO(6));
    expect(href.searchParams.get("end_date")).toBe(localDateISO());
    expect(exportLink).toHaveAttribute("download");

    await settle();
    expect(calls.keys).toHaveLength(1);
    expect(calls.keys[0].get("start_date")).toBe(daysAgoLocalISO(6));
    expect(calls.keys[0].has("group_id")).toBe(false);
    expect(calls.traffic).toHaveLength(0);
    for (const path of ["/api/accounts", "/api/request-logs/options", "/api/reports"]) {
      expect(calls.paths).not.toContain(path);
    }
  });

  it("keeps the quick period in the URL and clears its highlight when a date is edited", async () => {
    const user = userEvent.setup();
    const calls = mockApi();
    renderPage();
    await screen.findByTestId("key-report-summary");

    await user.click(screen.getByRole("button", { name: "30d" }));
    expect(screen.getByRole("button", { name: "30d" })).toHaveAttribute("aria-pressed", "true");
    expect(window.location.search).toBe("?period=30d");
    await waitFor(() => expect(calls.keys.at(-1)?.get("start_date")).toBe(daysAgoLocalISO(29)));

    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: daysAgoLocalISO(10) } });
    for (const label of ["Today", "Yesterday", "7d", "30d", "90d", "This month", "Last month"]) {
      expect(screen.getByRole("button", { name: label })).toHaveAttribute("aria-pressed", "false");
    }
    const search = new URLSearchParams(window.location.search);
    expect(search.get("from")).toBe(daysAgoLocalISO(10));
    expect(search.get("to")).toBe(localDateISO());
    expect(search.has("period")).toBe(false);
    await waitFor(() => expect(calls.keys.at(-1)?.get("start_date")).toBe(daysAgoLocalISO(10)));
  });

  it("bounds the date inputs, explains an inverted range without requesting it and resumes after a correction", async () => {
    const calls = mockApi();
    const today = localDateISO();
    const start = addDaysISO(today, -3);
    const end = addDaysISO(today, -8);
    renderPage(`/reports?from=${start}&to=${end}`);

    const startInput = screen.getByLabelText("Start date");
    const endInput = screen.getByLabelText("End date");
    expect(startInput).toHaveAttribute("max", end);
    expect(endInput).toHaveAttribute("min", start);
    expect(endInput).toHaveAttribute("max", today);
    expect(startInput).toHaveAttribute("aria-invalid", "true");
    expect(endInput).toHaveAttribute("aria-invalid", "true");
    const message = screen.getByText("Start date must be on or before end date.");
    expect(startInput.getAttribute("aria-describedby")).toBe(message.id);
    expect(endInput.getAttribute("aria-describedby")).toBe(message.id);
    expect(screen.getByRole("button", { name: "Export CSV" })).toBeDisabled();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Traffic details/ }));
    expect(screen.getByText("Choose a valid period to see traffic details.")).toBeInTheDocument();
    await settle();
    expect(calls.keys).toHaveLength(0);
    expect(calls.traffic).toHaveLength(0);

    const corrected = addDaysISO(today, -9);
    fireEvent.change(startInput, { target: { value: corrected } });
    await waitFor(() => expect(calls.keys).toHaveLength(1));
    expect(calls.keys[0].get("start_date")).toBe(corrected);
    expect(calls.keys[0].get("end_date")).toBe(end);
    expect(screen.queryByText("Start date must be on or before end date.")).not.toBeInTheDocument();
    expect(startInput).not.toHaveAttribute("aria-invalid");
    expect(endInput).not.toHaveAttribute("aria-describedby");
  });

  it("retries only the directory while the range is inverted", async () => {
    const user = userEvent.setup();
    const calls = mockApi({ directoryError: true });
    const today = localDateISO();
    renderPage(`/reports?from=${addDaysISO(today, -1)}&to=${addDaysISO(today, -2)}`);

    const alert = await screen.findByTestId("reports-directory-error");
    expect(alert).toHaveTextContent("Failed to load keys and groups: Directory unavailable");
    const before = calls.directory;
    await user.click(within(alert).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(calls.directory).toBe(before + 1));
    await settle();
    expect(calls.keys).toHaveLength(0);
    expect(calls.traffic).toHaveLength(0);
  });

  it("selects groups through the scope picker and writes them into the URL", async () => {
    const user = userEvent.setup();
    const calls = mockApi({ keys: (params) => ROWS.filter((key) => key.groupId !== null && params.getAll("group_id").includes(key.groupId)) });
    renderPage();
    await screen.findByTestId("key-report-summary");

    await user.click(screen.getByRole("button", { name: /Keys and groups/ }));
    const listbox = await screen.findByRole("listbox", { name: "Keys and groups" });
    await user.click(within(listbox).getByRole("option", { name: /^Team A/ }));
    await user.click(within(listbox).getByRole("option", { name: /^Team B/ }));
    expect(new URLSearchParams(window.location.search).getAll("group")).toEqual(["grp_a", "grp_b"]);
    await waitFor(() => expect(calls.keys.at(-1)?.getAll("group_id")).toEqual(["grp_a", "grp_b"]));
    expect(screen.getAllByTestId("scope-chip").map((chip) => chip.textContent)).toEqual(["Team A1 key", "Team B1 key"]);
  });

  it("shows a selected group without keys", async () => {
    mockApi({ keys: () => [] });
    renderPage("/reports?group=grp_empty");
    expect(await screen.findByText("This group has no keys")).toBeInTheDocument();
    expect(screen.getAllByText("No usage in this period").length).toBeGreaterThan(0);
  });

  it("keeps the previous report when a refetch fails and retries it", async () => {
    const user = userEvent.setup();
    let fail = false;
    const recorded: URLSearchParams[] = [];
    server.use(
      http.get("/api/directory", () => HttpResponse.json(DIRECTORY)),
      http.get("/api/reports/keys", ({ request }) => {
        const params = new URL(request.url).searchParams;
        recorded.push(params);
        if (fail) {
          return HttpResponse.json({ error: { code: "report_too_large", message: "The selection is too large." } }, { status: 400 });
        }
        return HttpResponse.json(buildKeyUsageReport({ start: params.get("start_date") ?? "", end: params.get("end_date") ?? "", keys: ROWS }));
      }),
    );
    renderPage();
    await screen.findByTestId("key-report-summary");
    fail = true;
    await user.click(screen.getByRole("button", { name: "90d" }));
    const alert = await screen.findByTestId("reports-error");
    expect(alert).toHaveTextContent("The selection is too large. Narrow the selection or shorten the period.");
    expect(screen.getByTestId("key-report-summary")).toBeInTheDocument();
    fail = false;
    await user.click(within(alert).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByTestId("reports-error")).not.toBeInTheDocument());
    expect(recorded.at(-1)?.get("start_date")).toBe(daysAgoLocalISO(89));
  });

  it("copies the page link, which carries the selection and the period", async () => {
    const user = userEvent.setup();
    mockApi();
    renderPage("/reports?group=grp_a&period=30d");
    await screen.findByTestId("key-report-summary");
    await user.click(screen.getByRole("button", { name: "Copy link" }));
    expect(copyToClipboard).toHaveBeenCalledWith(`${window.location.origin}/reports?group=grp_a&period=30d`);
  });

  describe("traffic details", () => {
    it("loads only when expanded, filtered by the resolved keys of a selection", async () => {
      const user = userEvent.setup();
      const calls = mockApi({ keys: () => ROWS.slice(0, 2) });
      renderPage("/reports?group=grp_a&group=grp_b");
      await screen.findByTestId("key-report-summary");
      await settle();
      expect(calls.traffic).toHaveLength(0);

      await user.click(screen.getByRole("button", { name: /Traffic details/ }));
      await waitFor(() => expect(calls.traffic).toHaveLength(1));
      expect(calls.traffic[0].getAll("api_key_id")).toEqual(["key_a", "key_b"]);
      expect(calls.traffic[0].get("start_date")).toBe(daysAgoLocalISO(6));
      expect(await screen.findByText("Distribution by Model")).toBeInTheDocument();
      expect(await screen.findByText("Distribution by UserAgent")).toBeInTheDocument();
    });

    it("requests every key unfiltered for All keys", async () => {
      const user = userEvent.setup();
      const calls = mockApi();
      renderPage();
      await user.click(screen.getByRole("button", { name: /Traffic details/ }));
      await waitFor(() => expect(calls.traffic).toHaveLength(1));
      expect(calls.traffic[0].has("api_key_id")).toBe(false);
    });

    it("asks to narrow a selection of more than 50 keys and requests nothing", async () => {
      const user = userEvent.setup();
      const many = Array.from({ length: 51 }, (_, index) => row(`k${index}`, `Key ${index}`, "grp_a", 1, 0.1));
      const calls = mockApi({ keys: () => many });
      renderPage("/reports?group=grp_a");
      await screen.findByTestId("key-report-summary");
      await user.click(screen.getByRole("button", { name: /Traffic details/ }));
      expect(
        screen.getByText("Traffic details cover at most 50 keys and this selection has 51. Narrow the selection to see them."),
      ).toBeInTheDocument();
      await settle();
      expect(calls.traffic).toHaveLength(0);
    });
  });
});
