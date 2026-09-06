import { screen, waitFor } from "@testing-library/react";
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
    await user.click(screen.getByRole("button", { name: "All accounts" }));
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
});
