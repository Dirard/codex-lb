import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";

import { server } from "@/test/mocks/server";
import { createModelSource } from "@/test/mocks/factories";
import { renderWithProviders } from "@/test/utils";

import { AntigravitySignInDialog } from "./antigravity-sign-in-dialog";
import { ModelSourcesSettings } from "./model-sources-settings";

const START = {
  flowId: "agsign_test",
  authorizationUrl: "https://accounts.google.com/o/oauth2/v2/auth?state=test-state",
  expiresAt: "2026-11-22T12:10:00Z",
};

function antigravitySource(overrides: Parameters<typeof createModelSource>[0] = {}) {
  return createModelSource({
    id: "src_antigravity",
    name: "agent@example.test",
    kind: "antigravity",
    baseUrl: "https://daily-cloudcode-pa.googleapis.com",
    supportsChatCompletions: true,
    supportsResponses: true,
    stickyOnly: true,
    accountStatus: "active",
    providerConfig: { antigravity: { email: "agent@example.test", project: "aicode-consumers", tier: "g1-pro-tier" } },
    ...overrides,
  });
}

describe("AntigravitySignInDialog", () => {
  it("opens Google's page in a new tab and completes with the pasted code", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    let posted: unknown = null;
    server.use(
      http.post("/api/antigravity/sign-in/:flowId", async ({ params, request }) => {
        posted = { flowId: params.flowId, body: await request.json() };
        return HttpResponse.json(antigravitySource());
      }),
    );
    renderWithProviders(<AntigravitySignInDialog start={START} onRestart={vi.fn()} onClose={onClose} />);

    const link = screen.getByRole("link", { name: "Open Google sign-in" });
    expect(link).toHaveAttribute("href", START.authorizationUrl);
    expect(link).toHaveAttribute("target", "_blank");
    expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();

    await user.type(screen.getByPlaceholderText("Paste the code here"), "4/synthetic-code");
    await user.click(screen.getByRole("button", { name: "Add account" }));

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(posted).toEqual({ flowId: "agsign_test", body: { code: "4/synthetic-code" } });
  });

  it("explains a refused code and offers a new sign-in", async () => {
    const user = userEvent.setup();
    const onRestart = vi.fn();
    server.use(
      http.post("/api/antigravity/sign-in/:flowId", () =>
        HttpResponse.json(
          { error: { code: "antigravity_code_rejected", message: "Google refused this code; start a new sign-in" } },
          { status: 400 },
        ),
      ),
    );
    renderWithProviders(<AntigravitySignInDialog start={START} reauthorizing onRestart={onRestart} onClose={vi.fn()} />);

    expect(screen.getByText("Re-authorize Antigravity account")).toBeInTheDocument();
    await user.type(screen.getByPlaceholderText("Paste the code here"), "4/mistyped");
    await user.click(screen.getByRole("button", { name: "Re-authorize" }));

    expect(await screen.findByText("Google refused this code; start a new sign-in")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Re-authorize" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Start again" }));
    expect(onRestart).toHaveBeenCalledTimes(1);
  });
});

describe("ModelSourcesSettings with Antigravity sources", () => {
  it("shows the account, its status and its verification link", async () => {
    server.use(
      http.get("/api/model-sources/", () =>
        HttpResponse.json({
          sources: [
            antigravitySource({
              accountStatus: "reauth_required",
              providerConfig: {
                antigravity: {
                  email: "agent@example.test",
                  project: "aicode-consumers",
                  tier: "g1-pro-tier",
                  actionUrl: "https://accounts.google.com/verify",
                },
              },
            }),
            antigravitySource({
              id: "src_suspended",
              name: "banned@example.test",
              accountStatus: "deactivated",
              deactivationReason: "tos_violation",
              providerConfig: {
                antigravity: { email: "banned@example.test", project: "aicode-consumers", actionUrl: "javascript:alert(1)" },
              },
            }),
          ],
        }),
      ),
    );
    renderWithProviders(<ModelSourcesSettings />);

    expect(await screen.findByText("Google Antigravity · agent@example.test")).toBeInTheDocument();
    expect(screen.getByText("Plan: g1-pro-tier")).toBeInTheDocument();
    expect(screen.getByText("Needs a new sign-in")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Verify the account" })).toHaveAttribute("href", "https://accounts.google.com/verify");
    expect(screen.getByText("Suspended by Google")).toBeInTheDocument();
    // An unsafe link from the server is never rendered.
    expect(screen.queryByRole("link", { name: "Submit an appeal" })).not.toBeInTheDocument();
    // The existing source controls stay.
    expect(screen.getByRole("switch", { name: "Keep conversations on agent@example.test when its limit runs out" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Edit agent@example.test model source" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete agent@example.test model source" })).toBeInTheDocument();
  });

  it("adds an account, re-authorizes and refreshes models", async () => {
    const user = userEvent.setup();
    const started: unknown[] = [];
    let refreshed = "";
    server.use(
      http.get("/api/model-sources/", () => HttpResponse.json({ sources: [antigravitySource()] })),
      http.post("/api/antigravity/sign-in", async ({ request }) => {
        started.push(await request.json());
        return HttpResponse.json(START);
      }),
      http.post("/api/model-sources/:sourceId/antigravity/refresh-models", ({ params }) => {
        refreshed = String(params.sourceId);
        return HttpResponse.json(antigravitySource());
      }),
    );
    renderWithProviders(<ModelSourcesSettings />);
    await screen.findByText("Google Antigravity · agent@example.test");

    await user.click(screen.getByRole("button", { name: "Refresh the models of agent@example.test" }));
    await waitFor(() => expect(refreshed).toBe("src_antigravity"));

    await user.click(screen.getByRole("button", { name: "Add Antigravity account" }));
    const addDialog = await screen.findByRole("dialog");
    expect(within(addDialog).getByText("Add Antigravity account")).toBeInTheDocument();
    expect(within(addDialog).getByRole("link", { name: "Open Google sign-in" })).toHaveAttribute("href", START.authorizationUrl);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Re-authorize agent@example.test" }));
    const reauthDialog = await screen.findByRole("dialog");
    expect(within(reauthDialog).getByText("Re-authorize Antigravity account")).toBeInTheDocument();
    expect(started).toEqual([{}, { sourceId: "src_antigravity" }]);
  });
});
