import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { MemoryRouter, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { GlobalSearch } from "@/features/search/components/global-search";
import { searchDirectory, SEARCH_RESULT_LIMIT } from "@/features/search/search-directory";
import type { Directory } from "@/features/search/api";
import { server } from "@/test/mocks/server";

const DIRECTORY: Directory = {
  keys: [
    { id: "key_alpha", name: "Alpha build bot", prefix: "sk-clb-AbC12", groupId: "grp_1", status: "active" },
    { id: "key_beta", name: "Beta", prefix: "sk-clb-Zz999", groupId: null, status: "disabled" },
  ],
  groups: [{ id: "grp_1", name: "Platform team", accountCount: 3, keyCount: 1 }],
  accounts: [
    { id: "acc_1", label: "Main Pro", email: "main@example.com", status: "active", kind: "chatgpt" },
    { id: "acc_2", label: "ops@example.com", email: "ops@example.com", status: "paused", kind: "chatgpt" },
  ],
};

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{`${location.pathname}${location.search}`}</div>;
}

function renderSearch() {
  server.use(http.get("/api/directory", () => HttpResponse.json(DIRECTORY)));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/overview"]}>
        <GlobalSearch />
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("searchDirectory", () => {
  it("matches key prefixes, account e-mails and group names case-insensitively, prefix matches first (R3)", () => {
    expect(searchDirectory(DIRECTORY, "SK-CLB-abc").map((r) => r.id)).toEqual(["key_alpha"]);
    expect(searchDirectory(DIRECTORY, "main@EXAMPLE").map((r) => r.id)).toEqual(["acc_1"]);
    expect(searchDirectory(DIRECTORY, "platform").map((r) => `${r.kind}:${r.id}`)).toEqual(["group:grp_1"]);
    expect(searchDirectory(DIRECTORY, "acc_2").map((r) => r.id)).toEqual(["acc_2"]);
    expect(searchDirectory(DIRECTORY, "bot").map((r) => r.id)).toEqual(["key_alpha"]);
    expect(searchDirectory(DIRECTORY, "   ")).toEqual([]);
  });

  it("offers a request id and caps results at 20", () => {
    const [first] = searchDirectory(DIRECTORY, "req_7Q2LK4ZXN5C3PW8R");
    expect(first).toMatchObject({ kind: "request", to: "/requests?request_id=req_7Q2LK4ZXN5C3PW8R" });
    const many: Directory = {
      ...DIRECTORY,
      keys: Array.from({ length: 40 }, (_, i) => ({ id: `k${i}`, name: `key ${i}`, prefix: `p${i}`, groupId: null, status: "active" as const })),
    };
    expect(searchDirectory(many, "key")).toHaveLength(SEARCH_RESULT_LIMIT);
  });
});

describe("GlobalSearch", () => {
  it("opens with Ctrl+K, groups results by kind and navigates to the selected key", async () => {
    const user = userEvent.setup();
    renderSearch();

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    const input = await screen.findByRole("combobox", { name: "Search" });
    await user.type(input, "sk-clb-abc");

    const keys = await screen.findByRole("group", { name: "Keys" });
    expect(within(keys).getByRole("option", { name: /Alpha build bot/ })).toBeInTheDocument();
    await user.keyboard("{Enter}");

    expect(screen.getByTestId("location")).toHaveTextContent("/keys/key_alpha");
  });

  it("finds accounts by e-mail and opens a request by id", async () => {
    const user = userEvent.setup();
    renderSearch();

    await user.click(screen.getAllByRole("button", { name: "Search keys, groups and accounts" })[0]);
    const input = await screen.findByRole("combobox", { name: "Search" });
    await user.type(input, "ops@");
    await user.click(await screen.findByRole("option", { name: /ops@example\.com/ }));
    expect(screen.getByTestId("location")).toHaveTextContent("/accounts?selected=acc_2");

    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const again = await screen.findByRole("combobox", { name: "Search" });
    await user.type(again, "req_7Q2LK4ZXN5C3PW8R");
    await user.click(await screen.findByRole("option", { name: /Open request req_7Q2LK4ZXN5C3PW8R/ }));
    expect(screen.getByTestId("location")).toHaveTextContent("/requests?request_id=req_7Q2LK4ZXN5C3PW8R");
  });
});
