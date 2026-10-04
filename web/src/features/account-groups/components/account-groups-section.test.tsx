import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { createAccountGroup, createAccountSummary } from "@/test/mocks/factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

import { AccountGroupsSection } from "./account-groups-section";

describe("AccountGroupsSection", () => {
  const group = createAccountGroup({
    id: "group_1",
    name: "Team pool",
    accountIds: ["acc_primary"],
    limits: [
      { limitType: "total_tokens", limitWindow: "weekly", maxValue: 100_000, modelFilter: null },
    ],
    keyCount: 2,
  });

  function mockGroups() {
    server.use(
      http.get("/api/accounts", () =>
        HttpResponse.json({ accounts: [createAccountSummary()] }),
      ),
      http.get("/api/account-groups/", () => HttpResponse.json([group])),
    );
  }

  it("renders groups with membership, limit, and key summaries", async () => {
    mockGroups();
    renderWithProviders(<AccountGroupsSection />);

    expect(await screen.findByText("Team pool")).toBeInTheDocument();
    expect(screen.getByText("2 keys")).toBeInTheDocument();
    expect(screen.getByText("1 accounts")).toBeInTheDocument();
    expect(screen.getByText("Tokens 100K/weekly")).toBeInTheDocument();
  });

  it("creates a group with accounts and limit rules", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockGroups();
    server.use(
      http.post("/api/account-groups/", async ({ request }) => {
        bodies.push(await request.json());
        return HttpResponse.json(createAccountGroup({ id: "group_2", name: "Pool" }));
      }),
    );

    renderWithProviders(<AccountGroupsSection />);
    await user.click(await screen.findByRole("button", { name: "Add group" }));
    await user.type(screen.getByLabelText("Name"), "Pool");
    await user.click(screen.getByRole("button", { name: "No accounts selected" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: /primary@example\.com/i }));
    await user.keyboard("{Escape}");
    await user.type(screen.getByLabelText("Weekly token limit"), "5000");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      expect(bodies).toHaveLength(1);
    });
    expect(bodies[0]).toEqual({
      name: "Pool",
      accountIds: ["acc_primary"],
      limits: [
        { limitType: "total_tokens", limitWindow: "weekly", maxValue: 5000, modelFilter: null },
      ],
    });
  });

  it("describes clearing membership as no accounts instead of unrestricted access", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockGroups();
    server.use(
      http.put("/api/account-groups/:groupId", async ({ request }) => {
        bodies.push(await request.json());
        return HttpResponse.json({ ...group, accountIds: [] });
      }),
    );
    renderWithProviders(<AccountGroupsSection />);
    await user.click(await screen.findByRole("button", { name: "Edit Team pool account group" }));
    await user.click(screen.getByRole("button", { name: "1 account selected" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "No accounts selected" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "No accounts selected" })).toBeChecked();
    expect(screen.queryByRole("menuitemcheckbox", { name: "All accounts" })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "No accounts selected" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ accountIds: [] });
  });

  it("edits a group with a full body", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockGroups();
    server.use(
      http.put("/api/account-groups/:groupId", async ({ request }) => {
        bodies.push(await request.json());
        return HttpResponse.json(group);
      }),
    );

    renderWithProviders(<AccountGroupsSection />);
    await user.click(
      await screen.findByRole("button", { name: "Edit Team pool account group" }),
    );
    const nameInput = screen.getByLabelText("Name");
    await user.clear(nameInput);
    await user.type(nameInput, "Team pool 2");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(bodies).toHaveLength(1);
    });
    expect(bodies[0]).toEqual({
      name: "Team pool 2",
      accountIds: ["acc_primary"],
      limits: [
        { limitType: "total_tokens", limitWindow: "weekly", maxValue: 100_000, modelFilter: null },
      ],
    });
  });

  it("requires a group name before submitting", async () => {
    const user = userEvent.setup();
    const create = vi.fn();
    mockGroups();
    server.use(
      http.post("/api/account-groups/", () => {
        create();
        return HttpResponse.json(createAccountGroup());
      }),
    );

    renderWithProviders(<AccountGroupsSection />);
    await user.click(await screen.findByRole("button", { name: "Add group" }));
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("Name is required")).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();
  });

  it("deletes a group after confirmation", async () => {
    const user = userEvent.setup();
    const deleted: string[] = [];
    mockGroups();
    server.use(
      http.delete("/api/account-groups/:groupId", ({ params }) => {
        deleted.push(String(params.groupId));
        return new HttpResponse(null, { status: 204 });
      }),
    );

    renderWithProviders(<AccountGroupsSection />);
    await user.click(
      await screen.findByRole("button", { name: "Delete Team pool account group" }),
    );
    await user.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => {
      expect(deleted).toEqual(["group_1"]);
    });
  });

  it("resets only after confirmation and prevents duplicate submissions", async () => {
    const user = userEvent.setup();
    const reset = vi.fn();
    const reads = vi.fn();
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    mockGroups();
    server.use(
      http.get("/api/account-groups/", () => { reads(); return HttpResponse.json([group]); }),
      http.post("/api/account-groups/:groupId/reset-usage", async ({ params }) => {
        reset(params.groupId);
        await pending;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderWithProviders(<AccountGroupsSection />);
    await user.click(await screen.findByRole("button", { name: "Reset accounting period for Team pool" }));
    expect(reset).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog")).toHaveTextContent("provider quotas and purchased credits will not change");
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    expect(reset).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Reset accounting period for Team pool" }));
    const confirm = within(screen.getByRole("alertdialog")).getByRole("button", { name: "Reset period" });
    try {
      await user.click(confirm);
      await waitFor(() => expect(confirm).toBeDisabled());
      expect(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel" })).toBeDisabled();
      await user.click(confirm);
      expect(reset).toHaveBeenCalledExactlyOnceWith("group_1");
    } finally {
      finish();
    }
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    await waitFor(() => expect(reads.mock.calls.length).toBeGreaterThan(1));
  });

  it("keeps the reset dialog open on failure and allows retry", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    mockGroups();
    server.use(http.post("/api/account-groups/:groupId/reset-usage", () => {
      attempts++;
      return attempts === 1
        ? HttpResponse.json({ error: { code: "reset_failed", message: "Reset failed" } }, { status: 500 })
        : new HttpResponse(null, { status: 204 });
    }));
    renderWithProviders(<AccountGroupsSection />);
    await user.click(await screen.findByRole("button", { name: "Reset accounting period for Team pool" }));
    const dialog = screen.getByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Reset period" }));
    expect(await within(dialog).findByText("Reset failed")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Reset period" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(attempts).toBe(2);
  });

  it("disables period reset for groups without limits or keys", async () => {
    server.use(http.get("/api/account-groups/", () => HttpResponse.json([
      { ...group, id: "empty", name: "Empty", keyCount: 0 },
      { ...group, id: "unlimited", name: "Unlimited", limits: [] },
    ])));
    renderWithProviders(<AccountGroupsSection />);
    expect(await screen.findByRole("button", { name: "Reset accounting period for Empty" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reset accounting period for Unlimited" })).toBeDisabled();
  });
});
