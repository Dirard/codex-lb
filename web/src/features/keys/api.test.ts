import { describe, expect, it } from "vitest";

import { DEFAULT_KEYS_PARAMS, hasKeyFilters, keysParamsFromSearch, keysSearch } from "@/features/keys/api";

describe("keys page URL state", () => {
  it("reads the parameters of GET /api/keys and writes back only non-defaults", () => {
    const params = keysParamsFromSearch(
      new URLSearchParams("q=bot&group=ungrouped&status=expired&near_limit=1&expiring_days=7&inactive_days=30&sort=spend_7d&order=asc&offset=100"),
    );
    expect(params).toEqual({ q: "bot", group: "ungrouped", status: "expired", nearLimit: true, expiringDays: 7, inactiveDays: 30,
      sort: "spend_7d", order: "asc", offset: 100 });
    expect(keysSearch(params).toString()).toBe(
      "q=bot&group=ungrouped&status=expired&near_limit=1&expiring_days=7&inactive_days=30&sort=spend_7d&order=asc&offset=100",
    );
    expect(keysSearch(DEFAULT_KEYS_PARAMS).toString()).toBe("");
    expect(hasKeyFilters(params)).toBe(true);
    expect(hasKeyFilters({ ...DEFAULT_KEYS_PARAMS, q: "  " })).toBe(false);
  });

  it("falls back to defaults for invalid values instead of sending them", () => {
    const params = keysParamsFromSearch(
      new URLSearchParams("status=paused&near_limit=yes&expiring_days=0&inactive_days=999&sort=cost&order=up&offset=-5&selected=key_1"),
    );
    expect(params).toEqual(DEFAULT_KEYS_PARAMS);
  });
});
