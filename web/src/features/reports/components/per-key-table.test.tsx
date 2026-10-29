import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  buildKeyUsageReport,
  createKeyReportRow,
  createUsageMeasures,
} from "@/test/mocks/key-usage-report-factories";
import { copyToClipboard } from "@/utils/clipboard";

import type { KeyReportRow, KeyUsageReport } from "../schemas";
import { buildKeySections, DEFAULT_PER_KEY_SORT } from "../key-sections";
import { GROUP_ROW_LIMIT, PerKeyTable } from "./per-key-table";

vi.mock("@/utils/clipboard", () => ({ copyToClipboard: vi.fn().mockResolvedValue(true) }));

function key(id: string, name: string, groupId: string | null, requests: number, costUsd: number, extra: Partial<KeyReportRow> = {}): KeyReportRow {
  return createKeyReportRow({
    id,
    name,
    prefix: `sk-${id}`,
    groupId,
    groupName: groupId ? `Group ${groupId}` : null,
    measures: createUsageMeasures({ requests, inputTokens: requests * 100, outputTokens: requests * 10, cachedInputTokens: requests * 5, costUsd, errors: Math.floor(requests / 10) }),
    lastUsedAt: requests > 0 ? "2026-10-28T10:00:00Z" : null,
    maxLimitPercent: requests > 0 ? requests / 2 : null,
    ...extra,
  });
}

function report(keys: KeyReportRow[], selection: Partial<KeyUsageReport["selection"]> = {}): KeyUsageReport {
  return buildKeyUsageReport({ start: "2026-10-01", end: "2026-10-31", keys, selection });
}

const MIXED = report([
  key("a1", "Alpha one", "A", 50, 5),
  key("a2", "Alpha two", "A", 10, 1),
  key("b1", "Beta one", "B", 100, 2),
  key("b2", "Beta idle", "B", 0, 0),
  key("u1", "Loose", null, 30, 3),
]);

function renderTable(value: KeyUsageReport) {
  return render(
    <MemoryRouter>
      <PerKeyTable report={value} />
    </MemoryRouter>,
  );
}

function rowNames(): string[] {
  return screen.getAllByTestId("per-key-row").map((row) => within(row).getAllByRole("link")[0].textContent ?? "");
}

function subtotalNames(): string[] {
  return screen.getAllByTestId("per-key-subtotal").map((row) => within(row).getByRole("button").textContent ?? "");
}

describe("buildKeySections", () => {
  it("groups keys with one subtotal per group, ungrouped included, ordered by cost", () => {
    const { sections, idleCount } = buildKeySections(MIXED, { filter: "", showIdle: false, sort: DEFAULT_PER_KEY_SORT, ungroupedLabel: "Ungrouped keys" });
    expect(sections.map((section) => section.name)).toEqual(["Group A", "Ungrouped keys", "Group B"]);
    expect(sections[0].subtotal.measures.requests).toBe(60);
    expect(sections[0].subtotal.measures.costUsd).toBe(6);
    expect(sections[0].rows.map((row) => row.id)).toEqual(["a1", "a2"]);
    expect(sections[2]).toMatchObject({ keyCount: 2, activeKeys: 1 });
    expect(sections[2].rows.map((row) => row.id)).toEqual(["b1"]);
    expect(idleCount).toBe(1);
  });

  it("filters by key name, prefix or group name and sorts by name with ungrouped keys last", () => {
    const byName = buildKeySections(MIXED, { filter: "", showIdle: true, sort: { column: "name", direction: "asc" }, ungroupedLabel: "Ungrouped keys" });
    expect(byName.sections.map((section) => section.name)).toEqual(["Group A", "Group B", "Ungrouped keys"]);
    expect(byName.sections[1].rows.map((row) => row.name)).toEqual(["Beta idle", "Beta one"]);
    const filtered = buildKeySections(MIXED, { filter: "group b", showIdle: false, sort: DEFAULT_PER_KEY_SORT, ungroupedLabel: "Ungrouped keys" });
    expect(filtered.sections.map((section) => section.id)).toEqual(["B"]);
    expect(filtered).toMatchObject({ idleCount: 1, matchCount: 2 });
    expect(buildKeySections(MIXED, { filter: "sk-u1", showIdle: false, sort: DEFAULT_PER_KEY_SORT, ungroupedLabel: "U" }).sections[0].rows[0].id).toBe("u1");
  });
});

describe("PerKeyTable", () => {
  beforeEach(() => {
    vi.mocked(copyToClipboard).mockClear();
  });

  it("renders keys grouped by group with subtotals, links and limits, hiding idle keys", async () => {
    const user = userEvent.setup();
    renderTable(MIXED);
    expect(screen.getByRole("heading", { name: "Keys" })).toBeInTheDocument();
    expect(subtotalNames()).toEqual(["Group A", "Ungrouped keys", "Group B"]);
    expect(rowNames()).toEqual(["Alpha one", "Alpha two", "Loose", "Beta one"]);
    const firstSubtotal = screen.getAllByTestId("per-key-subtotal")[0];
    expect(firstSubtotal).toHaveTextContent("2 of 2 keys active");
    expect(firstSubtotal).toHaveTextContent("$6.00");
    const first = screen.getAllByTestId("per-key-row")[0];
    expect(within(first).getAllByRole("link")[0]).toHaveAttribute("href", "/keys/a1");
    expect(within(first).getByRole("link", { name: "Open" })).toHaveAttribute("href", "/keys/a1");
    expect(first).toHaveTextContent("sk-a1");
    expect(first).toHaveTextContent("25%");
    // The group is the section header, not a column repeated on every row.
    expect(screen.queryByRole("columnheader", { name: "Group" })).not.toBeInTheDocument();
    expect(first).not.toHaveTextContent("Group A");

    await user.click(screen.getByRole("button", { name: "Show 1 idle key" }));
    expect(rowNames()).toContain("Beta idle");
    await user.click(screen.getByRole("button", { name: "Hide idle keys" }));
    expect(rowNames()).not.toContain("Beta idle");
  });

  it("sorts by every measure column and shows the sort state on the header", async () => {
    const user = userEvent.setup();
    renderTable(MIXED);
    const costHeader = screen.getByRole("columnheader", { name: /Cost/ });
    expect(costHeader).toHaveAttribute("aria-sort", "descending");
    await user.click(within(screen.getByRole("columnheader", { name: /^Requests/ })).getByRole("button"));
    expect(screen.getByRole("columnheader", { name: /^Requests/ })).toHaveAttribute("aria-sort", "descending");
    expect(costHeader).toHaveAttribute("aria-sort", "none");
    expect(subtotalNames()).toEqual(["Group B", "Group A", "Ungrouped keys"]);
    await user.click(within(screen.getByRole("columnheader", { name: /^Requests/ })).getByRole("button"));
    expect(screen.getByRole("columnheader", { name: /^Requests/ })).toHaveAttribute("aria-sort", "ascending");
    expect(rowNames()).toEqual(["Loose", "Alpha two", "Alpha one", "Beta one"]);
    await user.click(within(screen.getByRole("columnheader", { name: /^Key/ })).getByRole("button"));
    expect(screen.getByRole("columnheader", { name: /^Key/ })).toHaveAttribute("aria-sort", "ascending");
    for (const name of ["Input", "Output", "Cached", "Share", "Errors", "Error rate", "Last used", "Fullest limit"]) {
      const header = screen.getByRole("columnheader", { name: new RegExp(`^${name}`) });
      await user.click(within(header).getByRole("button"));
      expect(header).toHaveAttribute("aria-sort", "descending");
    }
  });

  it("filters, collapses a group and shows the first 20 rows of a large group until Show all", async () => {
    const user = userEvent.setup();
    const many = report(Array.from({ length: 45 }, (_, index) => key(`k${index}`, `Key ${String(index).padStart(2, "0")}`, "A", 100 - index, 100 - index)));
    renderTable(many);
    expect(screen.getAllByTestId("per-key-row")).toHaveLength(GROUP_ROW_LIMIT);
    await user.click(screen.getByRole("button", { name: "Show all 45" }));
    expect(screen.getAllByTestId("per-key-row")).toHaveLength(45);
    await user.click(screen.getByRole("button", { name: "Show fewer" }));
    expect(screen.getAllByTestId("per-key-row")).toHaveLength(GROUP_ROW_LIMIT);

    await user.click(screen.getByRole("button", { name: "Group A", expanded: true }));
    expect(screen.queryAllByTestId("per-key-row")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Group A", expanded: false }));

    await user.type(screen.getByRole("searchbox", { name: "Filter keys" }), "Key 4");
    expect(rowNames()).toEqual(["Key 40", "Key 41", "Key 42", "Key 43", "Key 44"]);
    await user.clear(screen.getByRole("searchbox", { name: "Filter keys" }));
    await user.type(screen.getByRole("searchbox", { name: "Filter keys" }), "nothing");
    expect(screen.getByText("No keys match the filter")).toBeInTheDocument();
  });

  it("copies the owner link of the report period without any credential", async () => {
    const user = userEvent.setup();
    renderTable(MIXED);
    await user.click(within(screen.getAllByTestId("per-key-row")[1]).getByRole("button", { name: "Copy owner link" }));
    expect(copyToClipboard).toHaveBeenCalledWith(`${window.location.origin}/?from=2026-10-01&to=2026-10-31`);
    expect(String(vi.mocked(copyToClipboard).mock.calls[0][0])).not.toMatch(/sk-|key|token/i);
  });

  it("explains an empty group, a selection without keys and a period without usage", async () => {
    const user = userEvent.setup();
    const { unmount } = renderTable(report([], { groupIds: ["grp_empty"], allKeys: false, resolvedKeyCount: 0 }));
    expect(screen.getByText("This group has no keys")).toBeInTheDocument();
    unmount();

    const second = renderTable(report([], { keyIds: ["gone"], allKeys: false, resolvedKeyCount: 0 }));
    expect(screen.getByText("No keys in the selection")).toBeInTheDocument();
    second.unmount();

    renderTable(report([key("i1", "Idle one", null, 0, 0), key("i2", "Idle two", null, 0, 0)]));
    expect(screen.getByText("No usage in this period")).toBeInTheDocument();
    expect(screen.getByText("2 idle keys are hidden.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show 2 idle keys" }));
    expect(rowNames()).toEqual(["Idle one", "Idle two"]);
  });
});
