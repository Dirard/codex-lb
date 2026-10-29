import { describe, expect, it } from "vitest";

import { TRAFFIC_DETAILS_KEY_LIMIT, trafficScope } from "./traffic-scope";

describe("trafficScope", () => {
  it("requests every key for All keys, the resolved keys up to 50, and nothing otherwise", () => {
    expect(trafficScope(true, null)).toEqual({ kind: "all" });
    expect(trafficScope(false, null)).toEqual({ kind: "pending" });
    expect(trafficScope(false, [])).toEqual({ kind: "none" });
    const fifty = Array.from({ length: TRAFFIC_DETAILS_KEY_LIMIT }, (_, index) => `key_${index}`);
    expect(trafficScope(false, fifty)).toEqual({ kind: "keys", keyIds: fifty });
    expect(trafficScope(false, [...fifty, "key_x"])).toEqual({ kind: "too_many", count: 51 });
  });
});
