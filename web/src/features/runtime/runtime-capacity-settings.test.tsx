import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";

import { RuntimeCapacitySettings } from "@/features/runtime/runtime-capacity-settings";
import { createRuntimeCapacity } from "@/test/mocks/handlers";
import { server } from "@/test/mocks/server";
import { formatBytes } from "@/utils/formatters";

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <RuntimeCapacitySettings />
    </QueryClientProvider>,
  );
}

describe("RuntimeCapacitySettings", () => {
  it("shows detected resources, limits and live usage", async () => {
    renderCard();
    expect(await screen.findByText(/6 CPU · 4.0 GiB available/)).toBeInTheDocument();
    expect(screen.getByText("12 / 2,048")).toBeInTheDocument();
    expect(screen.getByText("40 / 16,384")).toBeInTheDocument();
    expect(screen.getByText("3.0 GiB")).toBeInTheDocument();
    expect(screen.getByText("Request-body memory")).toBeInTheDocument();
    expect(screen.getByText("24 MiB / 512 MiB")).toBeInTheDocument();
    expect(screen.getByText("256 MiB")).toBeInTheDocument();
    expect(screen.getByText("All limits are derived automatically.")).toBeInTheDocument();
  });

  it("names operator overrides", async () => {
    server.use(http.get("/api/runtime/capacity", () => HttpResponse.json({ ...createRuntimeCapacity(), overridden: ["max_generations", "body_memory_budget"] })));
    renderCard();
    expect(await screen.findByText("Set by the operator: generations, request-body budget")).toBeInTheDocument();
  });

  it("hides itself on servers without the capacity endpoint", async () => {
    server.use(http.get("/api/runtime/capacity", () => HttpResponse.json({ error: { code: "not_found", message: "Not found" } }, { status: 404 })));
    const { container } = renderCard();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(container).toBeEmptyDOMElement();
  });

  it("formats byte sizes", () => {
    expect(formatBytes(0)).toBe("—");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536 * 1024)).toBe("1.5 MiB");
    expect(formatBytes(20 * 1024 ** 3)).toBe("20 GiB");
  });
});
