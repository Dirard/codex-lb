import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { createKeyReportRow, createKeyUsageReport, createUsageMeasures } from "@/test/mocks/key-usage-report-factories";

import { percentChange, percentChangeLabel } from "../report-format";
import { KeyReportSummary } from "./key-report-summary";

const CURRENT = createUsageMeasures({
  requests: 200,
  inputTokens: 1_500,
  outputTokens: 500,
  cachedInputTokens: 300,
  costUsd: 4,
  errors: 10,
});

function metric(id: string) {
  return screen.getByTestId(`key-report-metric-${id}`);
}

describe("KeyReportSummary", () => {
  it("shows totals, the token breakdown, the error rate and active keys", () => {
    const report = createKeyUsageReport({
      keys: [createKeyReportRow({ measures: CURRENT }), createKeyReportRow({ id: "key_2", measures: createUsageMeasures() })],
    });
    render(<KeyReportSummary report={report} />);
    expect(within(metric("requests")).getByText("Requests")).toBeInTheDocument();
    expect(metric("requests")).toHaveTextContent("200");
    expect(metric("tokens")).toHaveTextContent("Tokens");
    expect(metric("tokens")).toHaveTextContent("2,000");
    expect(within(metric("tokens")).getByText("Input 1,500 · Cache 300 · Output 500")).toBeInTheDocument();
    expect(metric("cost")).toHaveTextContent("Cost$4.00");
    expect(metric("errors")).toHaveTextContent("Errors10");
    expect(metric("errors")).toHaveTextContent("Error rate 5.0%");
    expect(metric("active-keys")).toHaveTextContent("Active keys1 of 2");
  });

  it("hides every indicator without a previous period", () => {
    render(<KeyReportSummary report={createKeyUsageReport({ keys: [createKeyReportRow({ measures: CURRENT })], previous: null })} />);
    expect(screen.queryAllByTestId("previous-period-change")).toHaveLength(0);
  });

  it("shows the change per metric and hides it where the previous value is zero", () => {
    const previous = createUsageMeasures({ requests: 100, inputTokens: 2_000, outputTokens: 2_000, costUsd: 5, errors: 0 });
    render(<KeyReportSummary report={createKeyUsageReport({ keys: [createKeyReportRow({ measures: CURRENT })], previous })} />);
    expect(within(metric("requests")).getByTestId("previous-period-change")).toHaveTextContent("▲ 100%");
    expect(within(metric("tokens")).getByTestId("previous-period-change")).toHaveTextContent("▼ 50%");
    expect(within(metric("cost")).getByTestId("previous-period-change")).toHaveTextContent("▼ 20%");
    expect(within(metric("cost")).getByTestId("previous-period-change")).toHaveTextContent("Previous period: $5.00");
    expect(within(metric("errors")).queryByTestId("previous-period-change")).toBeNull();
    expect(within(metric("active-keys")).queryByTestId("previous-period-change")).toBeNull();
  });

  it("labels increases, decreases and unchanged values", () => {
    expect(percentChange(5, 4)).toBe(25);
    expect(percentChange(5, 0)).toBeNull();
    expect(percentChange(5, null)).toBeNull();
    expect(percentChangeLabel(25)).toBe("▲ 25%");
    expect(percentChangeLabel(-3.24)).toBe("▼ 3.2%");
    expect(percentChangeLabel(0.01)).toBe("0%");
  });
});
