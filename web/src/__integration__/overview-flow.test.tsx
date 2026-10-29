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
    ["/apis?selected=key_1", "/keys/key_1", ""],
    ["/keys?selected=key_1", "/keys/key_1", ""],
    ["/apis?near_limit=1", "/keys", "?near_limit=1"],
  ])("redirects %s (R4)", async (from, pathname, search) => {
    window.history.pushState({}, "", from);
    renderWithProviders(<App />);
    await waitFor(() => expect(window.location.pathname).toBe(pathname));
    expect(window.location.search).toBe(search);
  });

  it("opens the Groups page for the Groups entry and old group settings links", async () => {
    window.history.pushState({}, "", "/settings?group=grp_1#account-groups");
    renderWithProviders(<App />);
    await waitFor(() => expect(window.location.pathname).toBe("/groups"));
    expect(window.location.search).toBe("?selected=grp_1");
    expect(await screen.findByRole("heading", { name: "Groups", level: 1 })).toBeInTheDocument();
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
