import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { AccountGroupsEditor } from "@/features/accounts/components/account-groups-editor";
import { createAccountGroup } from "@/test/mocks/factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const accountID = "acc_primary";

function mockGroups(accountIds: string[][]) {
  server.use(
    http.get("/api/account-groups/", () =>
      HttpResponse.json([
        createAccountGroup({ id: "group_1", name: "Team pool", accountIds: accountIds[0] ?? [] }),
        createAccountGroup({ id: "group_2", name: "Beta pool", accountIds: accountIds[1] ?? [] }),
      ]),
    ),
  );
}

describe("AccountGroupsEditor", () => {
  it("saves a multi-group selection in one request", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockGroups([[accountID], []]);
    server.use(
      http.put(`/api/accounts/${accountID}/groups`, async ({ request }) => {
        bodies.push(await request.json());
        return HttpResponse.json({ accountId: accountID, groupIds: ["group_1", "group_2"] });
      }),
    );

    renderWithProviders(<AccountGroupsEditor accountId={accountID} busy={false} />);

    expect(await screen.findByText("Team pool")).toBeInTheDocument();
    expect(screen.getByText("1 groups selected")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "Beta pool" }));
    expect(screen.getByText("2 groups selected")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save groups" }));

    await waitFor(() => {
      expect(bodies).toEqual([{ groupIds: ["group_1", "group_2"] }]);
    });
  });

  it("distinguishes loading, load errors, and an empty membership", async () => {
    server.use(http.get("/api/account-groups/", () => new Promise<Response>(() => {})));
    renderWithProviders(<AccountGroupsEditor accountId={accountID} busy={false} />);

    expect(screen.getByText("Loading groups…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();

    server.use(http.get("/api/account-groups/", () => new HttpResponse(null, { status: 500 })));
    const errorView = renderWithProviders(<AccountGroupsEditor accountId={accountID} busy={false} />);

    expect(await errorView.findByText("Request failed")).toBeInTheDocument();
    expect(within(errorView.container).getByRole("button", { name: "Save groups" })).toBeDisabled();

    mockGroups([[], []]);
    const emptyView = renderWithProviders(<AccountGroupsEditor accountId={accountID} busy={false} />);

    expect(await emptyView.findByText("Team pool")).toBeInTheDocument();
    expect(within(emptyView.container).getByText("No groups selected")).toBeInTheDocument();
    expect(within(emptyView.container).queryByText("No account groups yet")).not.toBeInTheDocument();
  });

  it("prevents busy and read-only writes and resets the draft on account switch", async () => {
    const user = userEvent.setup();
    mockGroups([[accountID], []]);
    const view = renderWithProviders(
      <AccountGroupsEditor key={accountID} accountId={accountID} busy={false} readOnly />,
    );

    expect(await screen.findByText("Team pool")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Team pool" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();

    view.rerender(
      <AccountGroupsEditor key={accountID} accountId={accountID} busy readOnly={false} />,
    );
    expect(screen.getByRole("checkbox", { name: "Team pool" })).toBeDisabled();

    view.rerender(
      <AccountGroupsEditor key={accountID} accountId={accountID} busy={false} readOnly={false} />,
    );
    await user.click(screen.getByRole("checkbox", { name: "Beta pool" }));
    expect(screen.getByText("2 groups selected")).toBeInTheDocument();

    view.rerender(
      <AccountGroupsEditor key="acc_secondary" accountId="acc_secondary" busy={false} readOnly={false} />,
    );

    expect(screen.getByRole("checkbox", { name: "Team pool" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Beta pool" })).not.toBeChecked();
    expect(screen.getByText("No groups selected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();
  });

  it("saves an explicit empty selection", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    mockGroups([[accountID], []]);
    server.use(
      http.put(`/api/accounts/${accountID}/groups`, async ({ request }) => {
        save(await request.json());
        return HttpResponse.json({ accountId: accountID, groupIds: [] });
      }),
    );

    renderWithProviders(<AccountGroupsEditor accountId={accountID} busy={false} />);

    await user.click(await screen.findByRole("checkbox", { name: "Team pool" }));
    await user.click(screen.getByRole("button", { name: "Save groups" }));

    await waitFor(() => {
      expect(save).toHaveBeenCalledWith({ groupIds: [] });
    });
  });

  it("keeps a failed draft and adopts refreshed memberships after success", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    const memberships: string[][] = [[accountID], []];
    server.use(
      http.get("/api/account-groups/", () =>
        HttpResponse.json([
          createAccountGroup({ id: "group_1", name: "Team pool", accountIds: memberships[0] }),
          createAccountGroup({ id: "group_2", name: "Beta pool", accountIds: memberships[1] }),
        ]),
      ),
      http.put(`/api/accounts/${accountID}/groups`, async ({ request }) => {
        bodies.push(await request.json());
        if (bodies.length === 1) {
          return new HttpResponse(null, { status: 500 });
        }
        memberships[0] = [];
        memberships[1] = [accountID];
        return HttpResponse.json({ accountId: accountID, groupIds: ["group_2"] });
      }),
    );

    const view = renderWithProviders(<AccountGroupsEditor accountId={accountID} busy={false} />);

    await user.click(await screen.findByRole("checkbox", { name: "Beta pool" }));
    await user.click(screen.getByRole("button", { name: "Save groups" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save groups" })).toBeEnabled();
    });
    expect(screen.getByRole("checkbox", { name: "Team pool" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Beta pool" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Save groups" }));

    expect(await screen.findByText("1 groups selected")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Team pool" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Beta pool" })).toBeChecked();
    expect(bodies).toEqual([{ groupIds: ["group_1", "group_2"] }, { groupIds: ["group_1", "group_2"] }]);

    memberships[1] = [];
    await view.queryClient.refetchQueries({ queryKey: ["account-groups", "list"] });

    await waitFor(() => {
      expect(screen.getByText("No groups selected")).toBeInTheDocument();
    });
    expect(screen.getByRole("checkbox", { name: "Team pool" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Beta pool" })).not.toBeChecked();
  });
});
