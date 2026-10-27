import { screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";

import { ApiKeyAuthWarning } from "@/features/api-keys/components/api-key-auth-warning";
import { createDashboardSettings } from "@/test/mocks/factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

describe("ApiKeyAuthWarning", () => {
  it("warns while API key authentication is disabled", async () => {
    server.use(http.get("/api/settings", () => HttpResponse.json(createDashboardSettings({ apiKeyAuthEnabled: false }))));
    renderWithProviders(<ApiKeyAuthWarning />);
    expect(await screen.findByRole("alert")).toHaveTextContent("API key authentication is off");
  });

  it("stays hidden while API key authentication is enabled", async () => {
    server.use(http.get("/api/settings", () => HttpResponse.json(createDashboardSettings({ apiKeyAuthEnabled: true }))));
    renderWithProviders(<ApiKeyAuthWarning />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
