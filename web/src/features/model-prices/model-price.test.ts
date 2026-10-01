import { describe, expect, it } from "vitest";
import { priceFromForm, rateFields } from "./model-price";

function form() {
  const data = new FormData();
  for (const [name] of rateFields) data.set(`standard.${name}`, "1.234567");
  data.set("priorityMultiplier", "2.5");
  return data;
}

describe("model price money conversion", () => {
  it("converts USD to exact microdollar rates and keeps optional tiers explicit", () => {
    const price = priceFromForm(form());
    expect(price.standard.inputMicrodollarsPerMillion).toBe(1234567);
    expect(price.priorityMultiplierMilli).toBe(2500);
    expect(price.priority).toBeUndefined();
    expect(price.longContext).toBeUndefined();
  });
  it("rejects missing, negative, non-finite and over-precision rates", () => {
    for (const value of ["", "-1", "Infinity", "0.0000001", "1000001"]) {
      const data = form();
      data.set("standard.inputMicrodollarsPerMillion", value);
      expect(() => priceFromForm(data)).toThrow();
    }
  });
});
