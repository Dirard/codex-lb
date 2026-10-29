import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Toaster } from "@/components/ui/sonner";
import { DEFAULT_KEYS_PARAMS } from "@/features/keys/api";
import { BulkActionsBar } from "@/features/keys/components/bulk-actions-bar";
import { useKeySelection } from "@/features/keys/hooks/use-keys-list";
import type { KeyRow } from "@/features/keys/schemas";
import { createApiKey } from "@/test/mocks/factories";
import { keyRowFromApiKey } from "@/test/mocks/keys-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const rows: KeyRow[] = [
  keyRowFromApiKey(createApiKey({ id: "key_a", name: "Alpha" }), []),
  keyRowFromApiKey(createApiKey({ id: "key_b", name: "Beta" }), []),
];

function Harness({ initial }: { initial: string[] }) {
  const selection = useKeySelection();
  const { replace } = selection;
  useEffect(() => replace(initial), [replace, initial]);
  return (
    <>
      <Toaster />
      <span data-testid="selection">{[...selection.ids].join(",")}</span>
      <BulkActionsBar selection={selection} total={12} params={DEFAULT_KEYS_PARAMS} timezone="UTC" rows={rows} />
    </>
  );
}

function recordBulk(): unknown[] {
  const bodies: unknown[] = [];
  server.events.on("request:start", async ({ request }) => {
    if (new URL(request.url).pathname === "/api/keys/bulk") bodies.push(await request.clone().json());
  });
  return bodies;
}

afterEach(() => {
  server.events.removeAllListeners("request:start");
});

describe("BulkActionsBar", () => {
  it("asks for a confirmation naming the action and the count before one bulk request (R39)", async () => {
    const user = userEvent.setup();
    const bodies = recordBulk();
    server.use(
      http.post("/api/keys/bulk", () => HttpResponse.json({ results: [{ id: "key_a", ok: true }, { id: "key_b", ok: true }] })),
    );
    const { queryClient } = renderWithProviders(<Harness initial={["key_a", "key_b"]} />);
    const invalidated = vi.spyOn(queryClient, "invalidateQueries");

    expect(await screen.findByTestId("keys-selected-count")).toHaveTextContent("2 selected");
    await user.click(screen.getByRole("button", { name: "Enable" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Enable 2 keys?" });
    expect(bodies).toHaveLength(0);
    await user.click(within(confirm).getByRole("button", { name: "Enable 2 keys" }));

    await waitFor(() => expect(bodies).toEqual([{ ids: ["key_a", "key_b"], action: "enable" }]));
    await waitFor(() => expect(screen.getByTestId("selection")).toBeEmptyDOMElement());
    expect(await screen.findByText("2 keys changed")).toBeInTheDocument();
    const keys = invalidated.mock.calls.map(([filters]) => JSON.stringify(filters?.queryKey));
    for (const key of [["keys"], ["directory"], ["overview"]]) {
      expect(keys).toContain(JSON.stringify(key));
    }
  });

  it("lists failed keys with their reason and keeps them selected", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/keys/bulk", () =>
        HttpResponse.json({ results: [{ id: "key_a", ok: true }, { id: "key_b", ok: false, code: "key_in_group" }] }),
      ),
    );
    renderWithProviders(<Harness initial={["key_a", "key_b"]} />);

    await user.click(await screen.findByRole("button", { name: "Set limits" }));
    const dialog = await screen.findByRole("dialog", { name: "Set limits for 2 keys" });
    await user.click(within(dialog).getByRole("button", { name: "Set limits for 2 keys" }));

    const failures = await screen.findByTestId("keys-bulk-failures");
    expect(failures).toHaveTextContent("Beta — in a group: limits come from the group");
    expect(screen.getByText("1 key was not changed and stays selected")).toBeInTheDocument();
    expect(screen.getByTestId("selection")).toHaveTextContent("key_b");
    expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("1 selected");
  });

  it("moves the selected keys out of their group after naming the count", async () => {
    const user = userEvent.setup();
    const bodies = recordBulk();
    renderWithProviders(<Harness initial={["key_a", "key_b"]} />);

    await user.click(await screen.findByRole("button", { name: "Move to group" }));
    const dialog = await screen.findByRole("dialog", { name: "Move 2 keys to a group" });
    expect(within(dialog).getByRole("button", { name: "Move 2 keys" })).toBeDisabled();
    await user.click(within(dialog).getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "No group" }));
    await user.click(within(dialog).getByRole("button", { name: "Move 2 keys" }));

    await waitFor(() => expect(bodies).toEqual([{ ids: ["key_a", "key_b"], action: "move_group", groupId: null }]));
  });

  it("extends the selection to every matching key", async () => {
    const user = userEvent.setup();
    server.use(http.get("/api/keys/ids", () => HttpResponse.json({ ids: ["key_a", "key_c", "key_d"], total: 1200 })));
    renderWithProviders(<Harness initial={["key_b"]} />);

    await user.click(await screen.findByRole("button", { name: "Select all 12 matching" }));
    await waitFor(() => expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("4 selected"));
    expect(await screen.findByText("Selected the first 3 of 1200 matching keys")).toBeInTheDocument();
  });
});
