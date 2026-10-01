import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";

import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";
import { ModelPricesSettings } from "./model-prices-settings";
import type { ModelPrice, ModelRepriceSummary } from "./model-price";

const entry: ModelPrice = {
  model: "gpt-new-model", source: "custom", hasBuiltin: false,
  price: { standard: { inputMicrodollarsPerMillion: 1000000, cachedMicrodollarsPerMillion: 100000, outputMicrodollarsPerMillion: 2000000 } },
};
const repriced: ModelRepriceSummary = {
  applied: true, recomputedRequests: 3, costDeltaMicrodollars: 1250000,
  partialRawRequests: 0, partialFoldedRequests: 0, partialFoldedBuckets: 0, undimensionedHistory: false,
};

describe("model prices settings", () => {
  it.each([false, true])("saves and displays the history recalculation result (partial=%s)", async (partial) => {
    const user = userEvent.setup();
    const saved = vi.fn();
    server.use(
      http.get("/api/model-prices", () => HttpResponse.json({ prices: [entry] })),
      http.put("/api/model-prices/gpt-new-model", async ({ request }) => {
        saved(await request.json());
        return HttpResponse.json({ ...entry, reprice: { ...repriced, partialRawRequests: partial ? 2 : 0, undimensionedHistory: partial } });
      }),
    );
    const { queryClient } = renderWithProviders(<ModelPricesSettings />);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    await user.click(await screen.findByRole("button", { name: "Edit gpt-new-model" }));
    const input = screen.getByRole("spinbutton", { name: "standard Input" });
    await user.clear(input);
    await user.type(input, "3.5");
    await user.click(screen.getByRole("button", { name: "Save and recalculate history" }));
    expect(await screen.findByRole("status")).toHaveTextContent("recalculated 3 requests. Cost adjustment: +$1.250000");
    expect(saved).toHaveBeenCalledWith({ price: { ...entry.price, standard: { ...entry.price.standard, inputMicrodollarsPerMillion: 3500000 } } });
    for (const key of ["model-prices", "dashboard", "reports", "accounts", "api-keys"]) {
      expect(invalidate).toHaveBeenCalledWith({ queryKey: [key] });
    }
    if (partial) {
      expect(screen.getByRole("status")).toHaveTextContent("Recalculation is partial: 2 individual requests");
      expect(screen.getByRole("status")).toHaveTextContent("without model-level details");
    } else {
      expect(screen.getByRole("status")).not.toHaveTextContent("partial");
    }
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("reports preserved costs when a custom-only price is removed", async () => {
    const user = userEvent.setup();
    let removed = false;
    server.use(
      http.get("/api/model-prices", () => HttpResponse.json({ prices: removed ? [] : [entry] })),
      http.delete("/api/model-prices/gpt-new-model", () => {
        removed = true;
        return HttpResponse.json({ reprice: { ...repriced, applied: false, recomputedRequests: 0, costDeltaMicrodollars: 0 } });
      }),
    );
    renderWithProviders(<ModelPricesSettings />);
    await user.click(await screen.findByRole("button", { name: "Remove price" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("Existing costs will not be erased");
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Historical costs were preserved");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Edit gpt-new-model" })).not.toBeInTheDocument());
  });
});
