import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it } from "vitest";

import App from "@/App";
import { createAccountGroup } from "@/features/account-groups/api";
import { updateApiKey } from "@/features/api-keys/api";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

afterEach(() => {
  server.events.removeAllListeners("request:start");
  window.history.pushState({}, "", "/");
});

type Seen = { method: string; path: string; body: unknown };

/** Records every request and the JSON body of every mutation. */
function recordRequests(): Seen[] {
  const seen: Seen[] = [];
  server.events.on("request:start", ({ request }) => {
    const entry: Seen = { method: request.method, path: new URL(request.url).pathname, body: null };
    seen.push(entry);
    if (request.method !== "GET" && request.headers.get("content-type")?.includes("json")) {
      void request
        .clone()
        .json()
        .then((body) => {
          entry.body = body;
        })
        .catch(() => undefined);
    }
  });
  return seen;
}

/** A group "Team" with the primary account and the default key. */
async function seedTeamGroup(): Promise<string> {
  const group = await createAccountGroup({ name: "Team", accountIds: ["acc_primary"], limits: [] });
  await updateApiKey("key_1", { groupId: group.id });
  return group.id;
}

async function openSheet(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(await screen.findByRole("button", { name }));
  return within(await screen.findByTestId("group-detail-sheet"));
}

describe("groups flow", () => {
  it("polls only the summary, loads the runway once and lists the groups (R1, R40)", async () => {
    await seedTeamGroup();
    const seen = recordRequests();
    window.history.pushState({}, "", "/groups");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "Groups", level: 1 })).toBeInTheDocument();
    const rows = await screen.findAllByTestId("groups-row");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("Team")).toBeInTheDocument();
    expect(within(rows[1]).getByText("Ungrouped keys")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Pool runway · weekly credits" })).toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    const paths = seen.map((entry) => entry.path);
    expect(paths.filter((path) => path === "/api/groups/summary")).toHaveLength(1);
    expect(paths.filter((path) => path === "/api/dashboard/projections")).toHaveLength(1);
    for (const path of ["/api/accounts", "/api/account-groups/", "/api/dashboard/status", "/health/ready"]) {
      expect(paths).not.toContain(path);
    }
  });

  it("sorts by a column and restores the default order", async () => {
    await seedTeamGroup();
    const user = userEvent.setup();
    window.history.pushState({}, "", "/groups");
    renderWithProviders(<App />);

    await screen.findAllByTestId("groups-row");
    await user.click(screen.getByRole("button", { name: /^Group$/ }));
    let rows = screen.getAllByTestId("groups-row");
    // Ascending by name: "Team" before "Ungrouped keys" (sorted by row name).
    expect(within(rows[0]).getByText("Team")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Group$/ }));
    rows = screen.getAllByTestId("groups-row");
    expect(within(rows[0]).getByText("Ungrouped keys")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Default order" }));
    rows = screen.getAllByTestId("groups-row");
    expect(within(rows[0]).getByText("Team")).toBeInTheDocument();
  });

  it("edits members and limits with one atomic save and keeps the draft across polls (R40)", async () => {
    const groupId = await seedTeamGroup();
    const user = userEvent.setup();
    const seen = recordRequests();
    window.history.pushState({}, "", "/groups");
    const { queryClient } = renderWithProviders(<App />);

    const sheet = await openSheet(user, "Team");
    expect(window.location.search).toBe(`?selected=${groupId}`);
    expect(sheet.getByRole("link", { name: "All keys of the group" })).toHaveAttribute("href", `/keys?group=${groupId}`);
    expect(sheet.getByRole("link", { name: "Default key" })).toHaveAttribute("href", "/keys/key_1");
    const secondary = sheet.getByRole("checkbox", { name: /secondary@example\.com/ });
    expect(sheet.getByRole("checkbox", { name: /primary@example\.com/ })).toBeChecked();
    await user.click(secondary);
    expect(secondary).toBeChecked();

    // A poll returns while the draft is unsaved: the selection stays.
    await act(async () => {
      await queryClient.refetchQueries({ queryKey: ["groups", "summary"] });
    });
    expect(within(screen.getByTestId("group-detail-sheet")).getByRole("checkbox", { name: /secondary@example\.com/ })).toBeChecked();
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();

    await user.type(sheet.getByLabelText("Weekly cost limit ($)"), "50");
    const before = seen.filter((entry) => entry.path === "/api/groups/summary").length;
    await user.click(sheet.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(seen.some((entry) => entry.method === "PUT" && entry.path === `/api/account-groups/${groupId}`)).toBe(true));
    const put = seen.find((entry) => entry.method === "PUT");
    await waitFor(() =>
      expect(put?.body).toEqual({
        name: "Team",
        accountIds: ["acc_primary", "acc_secondary"],
        limits: [{ limitType: "cost_usd", limitWindow: "weekly", maxValue: 50_000_000, modelFilter: null }],
      }),
    );
    expect(seen.filter((entry) => entry.method === "PUT" || entry.method === "POST")).toHaveLength(1);
    await waitFor(() => expect(seen.filter((entry) => entry.path === "/api/groups/summary").length).toBeGreaterThan(before));
    await waitFor(() => expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument());
    const row = (await screen.findAllByTestId("groups-row"))[0];
    await waitFor(() => expect(within(row).getByText("$50.00 / week")).toBeInTheDocument());
  });

  it("resets the period after confirmation and shows a refused delete", async () => {
    const groupId = await seedTeamGroup();
    const user = userEvent.setup();
    const seen = recordRequests();
    server.use(
      http.delete(`/api/account-groups/${groupId}`, () =>
        HttpResponse.json({ error: { code: "conflict", message: "Account group has linked API keys" } }, { status: 409 }),
      ),
    );
    window.history.pushState({}, "", `/groups?selected=${groupId}`);
    renderWithProviders(<App />);

    const sheet = within(await screen.findByTestId("group-detail-sheet"));
    await user.type(sheet.getByLabelText("Weekly cost limit ($)"), "10");
    await user.click(sheet.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sheet.getByRole("button", { name: "Reset period" })).toBeEnabled());

    await user.click(sheet.getByRole("button", { name: "Reset period" }));
    const confirm = within(await screen.findByRole("alertdialog"));
    expect(seen.some((entry) => entry.path.endsWith("/reset-usage"))).toBe(false);
    await user.click(confirm.getByRole("button", { name: "Reset period" }));
    await waitFor(() => expect(seen.some((entry) => entry.method === "POST" && entry.path === `/api/account-groups/${groupId}/reset-usage`)).toBe(true));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());

    await user.click(sheet.getByRole("button", { name: "Delete" }));
    const deleteDialog = within(await screen.findByRole("alertdialog"));
    await user.click(deleteDialog.getByRole("button", { name: "Delete" }));
    expect(await deleteDialog.findByText("Account group has linked API keys")).toBeInTheDocument();
    expect(screen.getByTestId("group-detail-sheet")).toBeInTheDocument();
  });

  it("creates a group from the toolbar and opens it", async () => {
    const user = userEvent.setup();
    const seen = recordRequests();
    window.history.pushState({}, "", "/groups");
    renderWithProviders(<App />);

    expect(await screen.findByText("No groups yet; ungrouped keys use the whole pool.")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "Create group" })[0]);
    const sheet = within(await screen.findByTestId("group-detail-sheet"));
    await user.type(sheet.getByLabelText("Name"), "Night shift");
    await user.click(sheet.getByRole("checkbox", { name: /primary@example\.com/ }));
    await user.click(sheet.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(seen.some((entry) => entry.method === "POST" && entry.path === "/api/account-groups/")).toBe(true));
    await waitFor(() => expect(window.location.search).toMatch(/^\?selected=group_/));
    expect(await screen.findByRole("heading", { name: "Night shift" })).toBeInTheDocument();
  });

  it("leaves Settings with the API-key access toggles and without key and group sections (R41)", async () => {
    const user = userEvent.setup();
    const seen = recordRequests();
    window.history.pushState({}, "", "/settings");
    renderWithProviders(<App />);

    expect(await screen.findByRole("heading", { name: "API-key access" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create key" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Account groups" })).not.toBeInTheDocument();
    expect(screen.queryByText("Dashboard refresh rate")).not.toBeInTheDocument();
    expect(screen.queryByText("Account burn projection")).not.toBeInTheDocument();
    expect(seen.map((entry) => entry.path)).not.toContain("/api/api-keys/");

    const section = screen.getByRole("heading", { name: "API-key access" }).closest("section") as HTMLElement;
    const switches = within(section).getAllByRole("switch");
    expect(switches).toHaveLength(2);
    await user.click(switches[0]);
    await waitFor(() => expect(seen.some((entry) => entry.method === "PUT" && entry.path === "/api/settings")).toBe(true));
    await waitFor(() => expect(seen.find((entry) => entry.method === "PUT")?.body).toEqual(expect.objectContaining({ apiKeyAuthEnabled: expect.any(Boolean) })));
  });
});
