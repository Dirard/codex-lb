import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import App from "@/App";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

afterEach(() => {
  server.events.removeAllListeners("request:start");
  window.history.pushState({}, "", "/");
});

describe("overview flow", () => {
  it.each([
    ["/", "/overview", ""],
    ["/dashboard", "/overview", ""],
    ["/apis?selected=key_1", "/keys", "?selected=key_1"],
  ])("redirects %s (R4)", async (from, pathname, search) => {
    window.history.pushState({}, "", from);
    renderWithProviders(<App />);
    await waitFor(() => expect(window.location.pathname).toBe(pathname));
    expect(window.location.search).toBe(search);
  });

  it("opens the group settings for the Groups entry", async () => {
    window.history.pushState({}, "", "/groups?selected=grp_1");
    renderWithProviders(<App />);
    await waitFor(() => expect(window.location.pathname).toBe("/settings"));
    expect(window.location.search).toBe("?group=grp_1");
    expect(window.location.hash).toBe("#account-groups");
  });

  it("renders the overview with one periodic request and no account or readiness polls (R1)", async () => {
    const requests: string[] = [];
    server.events.on("request:start", ({ request }) => requests.push(new URL(request.url).pathname));
    window.history.pushState({}, "", "/overview");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Pool" })).toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(requests).toContain("/api/overview");
    for (const path of ["/api/accounts", "/health/ready", "/api/dashboard/status", "/api/settings", "/api/dashboard/overview"]) {
      expect(requests).not.toContain(path);
    }
  });
});
