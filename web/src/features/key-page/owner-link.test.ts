import { describe, expect, it } from "vitest";

import { ownerKeyPageLink } from "./owner-link";

describe("ownerKeyPageLink", () => {
  it("opens the sign-in page for the period without any credential", () => {
    const link = ownerKeyPageLink({ from: "2026-10-01", to: "2026-10-29" }, "https://lb.example.test");
    expect(link).toBe("https://lb.example.test/?from=2026-10-01&to=2026-10-29");
    expect(link).not.toMatch(/key|token|sk-/i);
  });
});
