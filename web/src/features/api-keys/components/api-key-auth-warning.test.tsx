import { screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";

import { ApiKeyAuthWarning } from "@/features/api-keys/components/api-key-auth-warning";
import { createShellStatus } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

describe("ApiKeyAuthWarning", () => {
  it("warns while API key authentication is disabled, from the shell status", async () => {
    let settingsRequests = 0;
    server.use(
      http.get("/api/dashboard/status", () => HttpResponse.json(createShellStatus({ apiKeyAuthEnabled: false }))),
      http.get("/api/settings", () => {
        settingsRequests += 1;
        return HttpResponse.json({});
      }),
    );
    renderWithProviders(<ApiKeyAuthWarning />);
    expect(await screen.findByRole("alert")).toHaveTextContent("API key authentication is off");
    expect(settingsRequests).toBe(0);
  });

  it("stays hidden while API key authentication is enabled", async () => {
    server.use(http.get("/api/dashboard/status", () => HttpResponse.json(createShellStatus({ apiKeyAuthEnabled: true }))));
    renderWithProviders(<ApiKeyAuthWarning />);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
