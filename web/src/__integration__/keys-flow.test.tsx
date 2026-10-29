import { HttpResponse, http } from "msw";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "@/App";
import { createApiKey, createApiKeyCreateResponse } from "@/test/mocks/factories";
import { createKeysPage } from "@/test/mocks/keys-factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

type Recorded = { method: string; path: string; params: URLSearchParams; body?: unknown };

function recordRequests(): Recorded[] {
	const requests: Recorded[] = [];
	server.events.on("request:start", async ({ request }) => {
		const url = new URL(request.url);
		const entry: Recorded = { method: request.method, path: url.pathname, params: url.searchParams };
		requests.push(entry);
		if (request.method === "POST") entry.body = await request.clone().json();
	});
	return requests;
}

const keysRequests = (requests: Recorded[]) => requests.filter((entry) => entry.method === "GET" && entry.path === "/api/keys");

function getDialogFooterClose(dialog: HTMLElement): HTMLElement {
	return within(dialog)
		.getAllByRole("button", { name: "Close" })
		.find((button) => button.getAttribute("data-slot") === "button") as HTMLElement;
}

/** 70 keys served by the keys list of the mock server. */
function serveManyKeys() {
	const keys = Array.from({ length: 70 }, (_, index) =>
		createApiKey({ id: `key_${String(index).padStart(3, "0")}`, name: `Many ${String(index).padStart(3, "0")}`, keyPrefix: `sk-many-${index}` }),
	);
	server.use(
		http.get("/api/keys", ({ request }) => {
			const page = createKeysPage(new URL(request.url), keys, []);
			return "error" in page ? HttpResponse.json({ error: { code: page.error } }, { status: 400 }) : HttpResponse.json(page);
		}),
	);
	return keys;
}

afterEach(() => {
	server.events.removeAllListeners("request:start");
	vi.useRealTimers();
});

describe("keys page integration", () => {
	beforeEach(() => {
		window.history.pushState({}, "", "/keys");
	});

	it("lists keys from GET /api/keys with the shell status and without the old key list", async () => {
		const requests = recordRequests();
		renderWithProviders(<App />);

		expect(await screen.findByRole("heading", { name: "Keys" })).toBeInTheDocument();
		expect(await screen.findAllByTestId("keys-row")).toHaveLength(2);
		expect(screen.getByTestId("keys-total")).toHaveTextContent("2 keys");
		await act(async () => {
			await new Promise((resolve) => setTimeout(resolve, 50));
		});
		const paths = requests.map((entry) => entry.path);
		expect(paths).toContain("/api/keys");
		expect(paths).not.toContain("/api/api-keys/");
		expect(paths).not.toContain("/api/dashboard/status");
		expect(keysRequests(requests)[0].params.get("limit")).toBe("50");
		expect(keysRequests(requests)[0].params.get("timezone")).toBeTruthy();
	});

	it.each([
		["/keys?near_limit=1", "near_limit", "1", "Near limit (≥ 90%)"],
		["/keys?expiring_days=7", "expiring_days", "7", "Expiring within 7 days"],
	])("opens the overview link %s with its filter set", async (path, name, value, chip) => {
		const requests = recordRequests();
		window.history.pushState({}, "", path);
		renderWithProviders(<App />);

		expect(await screen.findByRole("button", { name: chip })).toHaveAttribute("aria-pressed", "true");
		await waitFor(() => expect(keysRequests(requests).at(-1)?.params.get(name)).toBe(value));
	});

	it("opens the overview's group link with that group and keeps filters in the URL", async () => {
		const user = userEvent.setup();
		const requests = recordRequests();
		window.history.pushState({}, "", "/keys?group=ungrouped");
		renderWithProviders(<App />);

		await waitFor(() => expect(keysRequests(requests).at(-1)?.params.get("group")).toBe("ungrouped"));
		expect(await screen.findByRole("combobox", { name: "Group" })).toHaveTextContent("No group");

		await user.type(screen.getByRole("textbox", { name: "Search by name or prefix" }), "default");
		await waitFor(() => expect(new URLSearchParams(window.location.search).get("q")).toBe("default"));
		await waitFor(() => expect(keysRequests(requests).at(-1)?.params.get("q")).toBe("default"));
		expect(new URLSearchParams(window.location.search).get("group")).toBe("ungrouped");

		await user.click(screen.getByRole("button", { name: "Reset filters" }));
		await waitFor(() => expect(window.location.search).toBe(""));
	});

	it("keeps the selection across a poll and a page change", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
		serveManyKeys();
		const requests = recordRequests();
		renderWithProviders(<App />);

		expect(await screen.findAllByTestId("keys-row")).toHaveLength(50);
		expect(screen.getByTestId("keys-page-of")).toHaveTextContent("Page 1 of 2");
		await user.click(screen.getByRole("checkbox", { name: "Select Many 000" }));
		await user.click(screen.getByRole("checkbox", { name: "Select Many 001" }));
		expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("2 selected");

		const before = keysRequests(requests).length;
		await act(async () => {
			await vi.advanceTimersByTimeAsync(61_000);
		});
		await waitFor(() => expect(keysRequests(requests).length).toBeGreaterThan(before));
		expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("2 selected");

		await user.click(screen.getByRole("button", { name: "Next" }));
		await waitFor(() => expect(new URLSearchParams(window.location.search).get("offset")).toBe("50"));
		expect(await screen.findByRole("checkbox", { name: "Select Many 060" })).toBeInTheDocument();
		expect(screen.getAllByTestId("keys-row")).toHaveLength(20);
		await user.click(screen.getByRole("checkbox", { name: "Select Many 060" }));
		expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("3 selected");

		await user.click(screen.getByRole("button", { name: "Previous" }));
		expect(await screen.findByRole("checkbox", { name: "Select Many 000" })).toBeChecked();
	});

	it("selects every matching key through GET /api/keys/ids", async () => {
		const user = userEvent.setup();
		serveManyKeys();
		server.use(http.get("/api/keys/ids", () => HttpResponse.json({ ids: Array.from({ length: 70 }, (_, i) => `key_${String(i).padStart(3, "0")}`), total: 70 })));
		renderWithProviders(<App />);

		await screen.findAllByTestId("keys-row");
		await user.click(screen.getByRole("checkbox", { name: "Select every key on this page" }));
		expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("50 selected");
		await user.click(screen.getByRole("button", { name: "Select all 70 matching" }));
		await waitFor(() => expect(screen.getByTestId("keys-selected-count")).toHaveTextContent("70 selected"));
	});

	it("disables the selected keys after a confirmation naming the action and the count", async () => {
		const user = userEvent.setup();
		const requests = recordRequests();
		renderWithProviders(<App />);

		await screen.findAllByTestId("keys-row");
		await user.click(screen.getByRole("checkbox", { name: "Select every key on this page" }));
		await user.click(within(screen.getByTestId("keys-bulk-bar")).getByRole("button", { name: "Disable" }));
		const confirm = await screen.findByRole("alertdialog", { name: "Disable 2 keys?" });
		expect(requests.some((entry) => entry.path === "/api/keys/bulk")).toBe(false);
		await user.click(within(confirm).getByRole("button", { name: "Disable 2 keys" }));

		await waitFor(() => expect(requests.filter((entry) => entry.path === "/api/keys/bulk")).toHaveLength(1));
		expect(requests.find((entry) => entry.path === "/api/keys/bulk")?.body).toEqual({ ids: ["key_1", "key_2"], action: "disable" });
		await waitFor(() => expect(screen.queryByTestId("keys-bulk-bar")).not.toBeInTheDocument());
		await waitFor(() => expect(screen.getAllByText("Disabled")).toHaveLength(2));
	});

	it("opens a key from its row", async () => {
		const user = userEvent.setup();
		renderWithProviders(<App />);

		await user.click(await screen.findByRole("link", { name: "Default key" }));
		await waitFor(() => expect(window.location.pathname).toBe("/keys/key_1"));
		expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();
		expect(screen.getByRole("group", { name: "Period" })).toBeInTheDocument();
	});

	it("redirects old bookmarks to the key page and to the Keys page with their filters", async () => {
		window.history.pushState({}, "", "/apis?selected=key_2");
		const view = renderWithProviders(<App />);
		expect(await screen.findByRole("heading", { name: "Read only key" })).toBeInTheDocument();
		expect(window.location.pathname).toBe("/keys/key_2");
		view.unmount();

		window.history.pushState({}, "", "/apis?status=disabled");
		renderWithProviders(<App />);
		expect(await screen.findByRole("heading", { name: "Keys" })).toBeInTheDocument();
		expect(window.location.pathname).toBe("/keys");
		expect(window.location.search).toBe("?status=disabled");
		expect(await screen.findAllByTestId("keys-row")).toHaveLength(1);
	});

	it("creates a key, shows the one-time key dialog, and refreshes the list", async () => {
		const user = userEvent.setup();
		renderWithProviders(<App />);

		await user.click(await screen.findByRole("button", { name: "Create key" }));
		const createDialog = await screen.findByRole("dialog", { name: "Create API key" });
		await user.type(within(createDialog).getByLabelText("Name"), "Created from Keys page");
		await user.click(within(createDialog).getByRole("button", { name: "Create" }));

		const dialog = await screen.findByRole("dialog", { name: "API key created" });
		expect(within(dialog).getByText(/sk-test-generated-/)).toBeInTheDocument();
		expect(within(dialog).getByText(/It will not be shown again/)).toBeInTheDocument();

		await user.click(getDialogFooterClose(dialog));
		expect(await screen.findByRole("link", { name: "Created from Keys page" })).toBeInTheDocument();
	});

	it("creates a key with a selectable reasoning-effort policy", async () => {
		const user = userEvent.setup();
		let requestBody: unknown;
		server.use(
			http.post("/api/api-keys/", async ({ request }) => {
				requestBody = await request.json();
				return HttpResponse.json(createApiKeyCreateResponse({ name: "Selectable effort key" }));
			}),
		);
		renderWithProviders(<App />);

		await user.click(await screen.findByRole("button", { name: "Create key" }));
		const createDialog = await screen.findByRole("dialog", { name: "Create API key" });
		await user.type(within(createDialog).getByLabelText("Name"), "Selectable effort key");
		await user.click(within(createDialog).getByRole("button", { name: "Allowed efforts: All efforts" }));
		await user.click(screen.getByRole("menuitemcheckbox", { name: /^Low$/ }));
		await user.keyboard("{Escape}");

		expect(within(createDialog).getByLabelText("Enforced Effort")).toBeDisabled();

		await user.click(within(createDialog).getByRole("button", { name: "Create" }));
		await waitFor(() => {
			expect(requestBody).toMatchObject({
				allowedReasoningEfforts: ["low"],
				enforcedReasoningEffort: null,
			});
		});
	});

	it("edits, toggles, regenerates, and deletes a key on its key page and returns to the Keys page", async () => {
		const user = userEvent.setup();
		window.history.pushState({}, "", "/keys/key_1");
		renderWithProviders(<App />);

		expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Edit" }));
		const editDialog = await screen.findByRole("dialog", { name: "Edit API key" });
		const nameInput = await within(editDialog).findByLabelText("Name");
		await user.clear(nameInput);
		await user.type(nameInput, "Updated from key page");
		await user.click(within(editDialog).getByRole("button", { name: "Save" }));
		expect(await screen.findByRole("heading", { name: "Updated from key page" })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Disable" }));
		await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Disable" }));
		expect(await screen.findByRole("button", { name: "Enable" })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "More key actions" }));
		await user.click(await screen.findByRole("menuitem", { name: "Regenerate" }));
		await user.click(within(await screen.findByRole("alertdialog", { name: "Regenerate this key?" })).getByRole("button", { name: "Regenerate" }));
		const regeneratedDialog = await screen.findByRole("dialog", { name: "API key created" });
		expect(within(regeneratedDialog).getByText(/sk-test-regenerated-key_1/)).toBeInTheDocument();
		await user.click(getDialogFooterClose(regeneratedDialog));

		await user.click(screen.getByRole("button", { name: "More key actions" }));
		await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
		const confirmDialog = await screen.findByRole("alertdialog", { name: "Delete API key" });
		await user.click(within(confirmDialog).getByRole("button", { name: "Delete" }));

		await waitFor(() => expect(window.location.pathname).toBe("/keys"));
		expect(await screen.findByRole("heading", { name: "Keys" })).toBeInTheDocument();
		expect(await screen.findAllByTestId("keys-row")).toHaveLength(1);
		expect(screen.queryByText("Updated from key page")).not.toBeInTheDocument();
	});

	it("disables a key from the row menu after a confirmation", async () => {
		const user = userEvent.setup();
		renderWithProviders(<App />);

		await user.click(await screen.findByRole("button", { name: "Actions for Default key" }));
		await user.click(await screen.findByRole("menuitem", { name: "Disable" }));
		await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Disable" }));
		await waitFor(() => expect(screen.getAllByText("Disabled")).toHaveLength(2));
		expect(window.location.pathname).toBe("/keys");
	});

	it("offers to create the first key without keys and to reset filters without matches", async () => {
		const user = userEvent.setup();
		server.use(http.get("/api/keys", ({ request }) => HttpResponse.json(createKeysPage(new URL(request.url), [], []))));
		window.history.pushState({}, "", "/keys?status=expired");
		renderWithProviders(<App />);

		expect(await screen.findByText("No keys match these filters")).toBeInTheDocument();
		await user.click(screen.getAllByRole("button", { name: "Reset filters" })[0]);
		expect(await screen.findByText("Create your first key")).toBeInTheDocument();
	});
});
