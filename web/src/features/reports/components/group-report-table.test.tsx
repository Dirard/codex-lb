import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { buildKeyUsageReport, createKeyReportRow, createUsageMeasures } from "@/test/mocks/key-usage-report-factories";

import { GroupReportTable } from "./group-report-table";

describe("GroupReportTable", () => {
  it("lists every group of a multi-group report with keys active of selected, tokens, cost share and errors", () => {
    const report = buildKeyUsageReport({
      start: "2026-10-01",
      end: "2026-10-07",
      keys: [
        createKeyReportRow({ id: "a", groupId: "grp_a", groupName: "Team A", measures: createUsageMeasures({ requests: 10, inputTokens: 100, outputTokens: 20, costUsd: 3, errors: 1 }) }),
        createKeyReportRow({ id: "b", groupId: "grp_a", groupName: "Team A", measures: createUsageMeasures() }),
        createKeyReportRow({ id: "c", groupId: null, measures: createUsageMeasures({ requests: 2, costUsd: 1 }) }),
      ],
    });
    render(<GroupReportTable groups={report.groups} />);
    expect(screen.getByRole("heading", { name: "Groups" })).toBeInTheDocument();
    const rows = screen.getAllByTestId("group-report-row");
    expect(within(rows[0]).getAllByRole("cell").map((cell) => cell.textContent)).toEqual([
      "Team A",
      "1 / 2",
      "10",
      "120",
      "$3.00",
      "75.0%",
      "1(10.0%)",
    ]);
    expect(within(rows[1]).getAllByRole("cell")[0]).toHaveTextContent("Ungrouped keys");
  });

  it("is hidden for a single group", () => {
    const report = buildKeyUsageReport({ start: "2026-10-01", end: "2026-10-07", keys: [createKeyReportRow()] });
    const { container } = render(<GroupReportTable groups={report.groups} />);
    expect(container).toBeEmptyDOMElement();
  });
});
