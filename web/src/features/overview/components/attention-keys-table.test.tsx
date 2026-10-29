import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";

import { extendedExpiry } from "@/features/overview/components/overview-actions";
import { OverviewPage } from "@/features/overview/components/overview-page";
import { createApiKey } from "@/test/mocks/factories";
import { createAttentionKey, createOverview } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const EXPIRES = new Date(Date.now() + 2 * 24 * 3_600_000).toISOString();

function mockOverview() {
  let overviewRequests = 0;
  const patches: Array<{ id: string; body: unknown }> = [];
  server.use(
    http.get("/api/overview", () => {
      overviewRequests += 1;
      return HttpResponse.json(
        createOverview({
          attentionKeys: [createAttentionKey({ id: "key_1", name: "Default key", expiresAt: EXPIRES })],
          attentionKeysTotal: 3,
        }),
      );
    }),
    http.patch("/api/api-keys/:keyId", async ({ params, request }) => {
      patches.push({ id: String(params.keyId), body: await request.json() });
      return HttpResponse.json(createApiKey({ id: String(params.keyId) }));
    }),
  );
  return { patches, overviewRequests: () => overviewRequests };
}

describe("AttentionKeysTable", () => {
  it("computes the extension from the later of now and the current expiry", () => {
    const now = Date.parse("2026-10-28T12:00:00Z");
    expect(extendedExpiry("2026-10-30T12:00:00Z", now)).toBe("2026-11-29T12:00:00.000Z");
    expect(extendedExpiry("2026-10-01T00:00:00Z", now)).toBe("2026-11-27T12:00:00.000Z");
    expect(extendedExpiry(null, now)).toBe("2026-11-27T12:00:00.000Z");
  });

  it("extends by 30 days after confirmation and refetches the overview (R17)", async () => {
    const user = userEvent.setup();
    const mock = mockOverview();
    renderWithProviders(<OverviewPage />);

    const row = await screen.findByTestId("overview-attention-key");
    expect(screen.getByText("3 keys need attention")).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: "Extend 30 days" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Extend Default key by 30 days?" });
    expect(mock.patches).toEqual([]);
    await user.click(within(dialog).getByRole("button", { name: "Extend" }));

    await waitFor(() => expect(mock.patches).toHaveLength(1));
    const body = mock.patches[0].body as { expiresAt: string };
    expect(mock.patches[0].id).toBe("key_1");
    expect(Object.keys(body)).toEqual(["expiresAt"]);
    expect(Date.parse(body.expiresAt) - Date.parse(EXPIRES)).toBe(30 * 24 * 3_600_000);
    await waitFor(() => expect(mock.overviewRequests()).toBeGreaterThanOrEqual(2));
  });

  it("disables a key after confirmation through the row menu", async () => {
    const user = userEvent.setup();
    const mock = mockOverview();
    renderWithProviders(<OverviewPage />);

    const row = await screen.findByTestId("overview-attention-key");
    await user.click(within(row).getByRole("button", { name: "More actions for Default key" }));
    await user.click(await screen.findByRole("menuitem", { name: "Disable" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Disable Default key?" });
    await user.click(within(dialog).getByRole("button", { name: "Disable" }));

    await waitFor(() => expect(mock.patches).toEqual([{ id: "key_1", body: { isActive: false } }]));
    await waitFor(() => expect(mock.overviewRequests()).toBeGreaterThanOrEqual(2));
  });

  it("opens the key page from the name and from the row menu", async () => {
    const user = userEvent.setup();
    mockOverview();
    renderWithProviders(<OverviewPage />);

    const row = await screen.findByTestId("overview-attention-key");
    expect(within(row).getByRole("link", { name: "Default key" })).toHaveAttribute("href", "/keys/key_1");
    await user.click(within(row).getByRole("button", { name: "More actions for Default key" }));
    await user.click(await screen.findByRole("menuitem", { name: "Open" }));
    await waitFor(() => expect(window.location.pathname).toBe("/keys/key_1"));
  });

  it("cancelling the confirmation sends nothing", async () => {
    const user = userEvent.setup();
    const mock = mockOverview();
    renderWithProviders(<OverviewPage />);

    const row = await screen.findByTestId("overview-attention-key");
    await user.click(within(row).getByRole("button", { name: "Extend 30 days" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(mock.patches).toEqual([]);
  });
});
