import { describe, expect, it } from "vitest";

import { getKeyUsageReport, keyUsageReportExportUrl } from "@/features/reports/api";
import { KEY_USAGE_CSV_HEADER } from "@/test/mocks/key-usage-report-factories";

const PERIOD = { startDate: "2026-10-01", endDate: "2026-10-07", timezone: "UTC" };

describe("key usage report mocks", () => {
  it("resolves every mock key for an empty selection and only the selected keys otherwise", async () => {
    const all = await getKeyUsageReport({ groupIds: [], keyIds: [], ungrouped: false, ...PERIOD });
    expect(all.selection).toMatchObject({ allKeys: true, resolvedKeyCount: 2 });
    expect(all.daily.map((day) => day.date)).toEqual(["2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04", "2026-10-05", "2026-10-06", "2026-10-07"]);
    expect(all.summary.requests).toBe(192);

    const one = await getKeyUsageReport({ groupIds: [], keyIds: ["key_2"], ungrouped: false, ...PERIOD });
    expect(one.keys.map((key) => key.id)).toEqual(["key_2"]);
    expect(one.keys[0].status).toBe("disabled");

    const group = await getKeyUsageReport({ groupIds: ["missing"], keyIds: [], ungrouped: false, ...PERIOD });
    expect(group.selection.resolvedKeyCount).toBe(0);
  });

  it("exports a CSV with the documented header", async () => {
    const response = await fetch(keyUsageReportExportUrl({ groupIds: [], keyIds: [], ungrouped: true, ...PERIOD }));
    expect(response.headers.get("content-type")).toMatch(/^text\/csv/);
    const lines = (await response.text()).trim().split("\n");
    expect(lines[0]).toBe(KEY_USAGE_CSV_HEADER);
    expect(lines).toHaveLength(3);
  });
});
