import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { AccountGroupsEditor } from "@/features/accounts/components/account-groups-editor";
import { createAccountGroup } from "@/test/mocks/factories";
import { createDirectory } from "@/test/mocks/overview-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const accountID = "acc_primary";

function mockDirectoryGroups() {
  server.use(
    http.get("/api/directory", () =>
      HttpResponse.json(
        createDirectory([], [], [
          createAccountGroup({ id: "group_1", name: "Team pool", accountIds: [] }),
          createAccountGroup({ id: "group_2", name: "Beta pool", accountIds: [] }),
        ]),
      ),
    ),
  );
}

describe("AccountGroupsEditor", () => {
  it("saves a multi-group selection in one request", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockDirectoryGroups();
    server.use(
      http.put(`/api/accounts/${accountID}/groups`, async ({ request }) => {
        bodies.push(await request.json());
        return HttpResponse.json({ accountId: accountID, groupIds: ["group_1", "group_2"] });
      }),
    );

    renderWithProviders(<AccountGroupsEditor accountId={accountID} currentGroupIds={["group_1"]} busy={false} />);

    expect(await screen.findByText("Team pool")).toBeInTheDocument();
    expect(screen.getByText("1 group selected")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "Beta pool" }));
    expect(screen.getByText("2 groups selected")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save groups" }));

    await waitFor(() => {
      expect(bodies).toEqual([{ groupIds: ["group_1", "group_2"] }]);
    });
  });

  it("distinguishes loading, load errors, and an empty membership", async () => {
    server.use(http.get("/api/directory", () => new Promise<Response>(() => {})));
    const loadingView = renderWithProviders(<AccountGroupsEditor accountId={accountID} currentGroupIds={[]} busy={false} />);

    expect(screen.getByText("Loading groups…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();
    loadingView.unmount();

    server.use(http.get("/api/directory", () => new HttpResponse(null, { status: 500 })));
    const errorView = renderWithProviders(<AccountGroupsEditor accountId={accountID} currentGroupIds={[]} busy={false} />);

    expect(await errorView.findByText("Request failed")).toBeInTheDocument();
    expect(within(errorView.container).getByRole("button", { name: "Save groups" })).toBeDisabled();
    errorView.unmount();

    mockDirectoryGroups();
    const emptyView = renderWithProviders(<AccountGroupsEditor accountId={accountID} currentGroupIds={[]} busy={false} />);

    expect(await emptyView.findByText("Team pool")).toBeInTheDocument();
    expect(within(emptyView.container).getByText("No groups selected")).toBeInTheDocument();
    expect(within(emptyView.container).queryByText("No account groups yet")).not.toBeInTheDocument();
  });

  it("prevents busy writes and resets the draft on account switch", async () => {
    const user = userEvent.setup();
    mockDirectoryGroups();
    const view = renderWithProviders(
      <AccountGroupsEditor key={accountID} accountId={accountID} currentGroupIds={["group_1"]} busy />,
    );

    expect(await screen.findByText("Team pool")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Team pool" })).toBeDisabled();

    view.rerender(<AccountGroupsEditor key={accountID} accountId={accountID} currentGroupIds={["group_1"]} busy={false} />);
    await user.click(screen.getByRole("checkbox", { name: "Beta pool" }));
    expect(screen.getByText("2 groups selected")).toBeInTheDocument();

    view.rerender(<AccountGroupsEditor key="acc_secondary" accountId="acc_secondary" currentGroupIds={[]} busy={false} />);

    expect(screen.getByRole("checkbox", { name: "Team pool" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Beta pool" })).not.toBeChecked();
    expect(screen.getByText("No groups selected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();
  });

  it("saves an explicit empty selection", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    mockDirectoryGroups();
    server.use(
      http.put(`/api/accounts/${accountID}/groups`, async ({ request }) => {
        save(await request.json());
        return HttpResponse.json({ accountId: accountID, groupIds: [] });
      }),
    );

    renderWithProviders(<AccountGroupsEditor accountId={accountID} currentGroupIds={["group_1"]} busy={false} />);

    await user.click(await screen.findByRole("checkbox", { name: "Team pool" }));
    await user.click(screen.getByRole("button", { name: "Save groups" }));

    await waitFor(() => {
      expect(save).toHaveBeenCalledWith({ groupIds: [] });
    });
  });

  it("keeps a failed draft and shows the saved membership after success", async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockDirectoryGroups();
    server.use(
      http.put(`/api/accounts/${accountID}/groups`, async ({ request }) => {
        bodies.push(await request.json());
        if (bodies.length === 1) {
          return new HttpResponse(null, { status: 500 });
        }
        return HttpResponse.json({ accountId: accountID, groupIds: ["group_2"] });
      }),
    );

    const view = renderWithProviders(<AccountGroupsEditor accountId={accountID} currentGroupIds={["group_1"]} busy={false} />);

    await user.click(await screen.findByRole("checkbox", { name: "Team pool" }));
    await user.click(screen.getByRole("checkbox", { name: "Beta pool" }));
    await user.click(screen.getByRole("button", { name: "Save groups" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save groups" })).toBeEnabled();
    });
    expect(screen.getByRole("checkbox", { name: "Team pool" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Beta pool" })).toBeChecked();

    await user.click(screen.getByRole("button", { name: "Save groups" }));
    await waitFor(() => {
      expect(bodies).toEqual([{ groupIds: ["group_2"] }, { groupIds: ["group_2"] }]);
    });

    // The list refetch delivers the saved membership as the current one.
    view.rerender(<AccountGroupsEditor accountId={accountID} currentGroupIds={["group_2"]} busy={false} />);
    expect(await screen.findByText("1 group selected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save groups" })).toBeDisabled();
  });
});
