import { describe, expect, it } from "vitest";

import { nextSort, sortGroupRows } from "@/features/groups/groups-sort";
import type { GroupSummaryRow } from "@/features/groups/schemas";
import { createGroupRow } from "@/test/mocks/overview-factories";

function row(id: string | null, name: string, overrides: Partial<GroupSummaryRow> = {}): GroupSummaryRow {
  return { ...createGroupRow({ id, name }), spend30dUsd: null, limits: [], accountIds: [], topKeys7d: [], ...overrides };
}

describe("sortGroupRows", () => {
  const rows = [
    row("g-b", "Beta", { spend30dUsd: 5, accountIds: ["a"] }),
    row(null, "Ungrouped keys", { spend30dUsd: null }),
    row("g-a", "alpha", { spend30dUsd: 9, accountIds: ["a", "b", "c"] }),
  ];

  it("keeps the server order without a sort", () => {
    expect(sortGroupRows(rows, null)).toBe(rows);
  });

  it("sorts figures with unknown values last in both directions", () => {
    expect(sortGroupRows(rows, { key: "spend30d", direction: "desc" }).map((r) => r.name)).toEqual(["alpha", "Beta", "Ungrouped keys"]);
    expect(sortGroupRows(rows, { key: "spend30d", direction: "asc" }).map((r) => r.name)).toEqual(["Beta", "alpha", "Ungrouped keys"]);
  });

  it("sorts names case-insensitively and accounts by members", () => {
    expect(sortGroupRows(rows, { key: "name", direction: "asc" }).map((r) => r.name)).toEqual(["alpha", "Beta", "Ungrouped keys"]);
    expect(sortGroupRows(rows, { key: "accounts", direction: "desc" })[0].name).toBe("alpha");
  });

  it("cycles a column through its first direction, the other and the default order", () => {
    expect(nextSort(null, "spendToday")).toEqual({ key: "spendToday", direction: "desc" });
    expect(nextSort({ key: "spendToday", direction: "desc" }, "spendToday")).toEqual({ key: "spendToday", direction: "asc" });
    expect(nextSort({ key: "spendToday", direction: "asc" }, "spendToday")).toBeNull();
    expect(nextSort({ key: "spendToday", direction: "asc" }, "name")).toEqual({ key: "name", direction: "asc" });
  });
});
