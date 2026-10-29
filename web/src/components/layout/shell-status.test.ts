import { describe, expect, it } from "vitest";

import { pageCarriesShellStatus } from "@/components/layout/shell-status";
import { localDateISO, shiftISODate } from "@/features/key-page/period";

describe("pageCarriesShellStatus", () => {
  it("is carried by the overview poll", () => {
    expect(pageCarriesShellStatus("/overview")).toBe(true);
    expect(pageCarriesShellStatus("/overview/")).toBe(true);
    expect(pageCarriesShellStatus("/settings")).toBe(false);
    expect(pageCarriesShellStatus("/keys")).toBe(false);
  });

  it("is carried by a key page only while its period includes today", () => {
    const today = localDateISO();
    expect(pageCarriesShellStatus("/keys/key_1")).toBe(true);
    expect(pageCarriesShellStatus("/keys/key_1", `?from=${shiftISODate(today, -29)}&to=${today}`)).toBe(true);
    expect(pageCarriesShellStatus("/keys/key_1", `?from=${shiftISODate(today, -14)}&to=${shiftISODate(today, -1)}`)).toBe(false);
    expect(pageCarriesShellStatus("/keys/key_1/extra", "")).toBe(false);
  });
});
