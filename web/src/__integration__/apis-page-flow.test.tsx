import { HttpResponse, http } from "msw";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import App from "@/App";
import { createApiKeyCreateResponse } from "@/test/mocks/factories";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

function getDialogFooterClose(dialog: HTMLElement): HTMLElement {
	return within(dialog)
		.getAllByRole("button", { name: "Close" })
		.find((button) => button.getAttribute("data-slot") === "button") as HTMLElement;
}

describe("apis page integration", () => {
	beforeEach(() => {
		window.history.pushState({}, "", "/apis");
	});

	it("filters the key list by search and opens the key page of a selected key", async () => {
		const user = userEvent.setup();
		renderWithProviders(<App />);

		expect(await screen.findByRole("heading", { name: "APIs" })).toBeInTheDocument();
		expect(await screen.findByText("Overview")).toBeInTheDocument();
		expect(screen.queryByTestId("api-key-info")).not.toBeInTheDocument();

		await user.type(screen.getByPlaceholderText("Search API keys..."), "Default");
		expect(screen.queryByRole("button", { name: /Read only key/i })).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /Default key/i }));

		await waitFor(() => expect(window.location.pathname).toBe("/keys/key_1"));
		expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();
		expect(screen.getByRole("group", { name: "Period" })).toBeInTheDocument();
	});

	it("opens the key page for an old selection link", async () => {
		window.history.pushState({}, "", "/apis?selected=key_2");
		renderWithProviders(<App />);

		expect(await screen.findByRole("heading", { name: "Read only key" })).toBeInTheDocument();
		expect(window.location.pathname).toBe("/keys/key_2");
		expect(screen.getByRole("button", { name: "Enable" })).toBeInTheDocument();
	});

	it("creates a key, shows the one-time key dialog, and refreshes the list", async () => {
		const user = userEvent.setup();
		renderWithProviders(<App />);

		await user.click(await screen.findByRole("button", { name: "Create API Key" }));
		const createDialog = await screen.findByRole("dialog", { name: "Create API key" });
		await user.type(within(createDialog).getByLabelText("Name"), "Created from APIs page");
		await user.click(within(createDialog).getByRole("button", { name: "Create" }));

		const dialog = await screen.findByRole("dialog", { name: "API key created" });
		expect(within(dialog).getByText(/sk-test-generated-/)).toBeInTheDocument();
		expect(within(dialog).getByText(/It will not be shown again/)).toBeInTheDocument();

		await user.click(getDialogFooterClose(dialog));
		expect(await screen.findByRole("button", { name: /Created from APIs page/i })).toBeInTheDocument();
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

		await user.click(await screen.findByRole("button", { name: "Create API Key" }));
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

	it("edits, toggles, regenerates, and deletes a key on its key page", async () => {
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
		expect(await screen.findByRole("heading", { name: "APIs" })).toBeInTheDocument();
		await waitFor(() => {
			expect(screen.queryByText("Updated from key page")).not.toBeInTheDocument();
		});
	});

	it("shows backend errors from API mutations in the page alert", async () => {
		const user = userEvent.setup();
		server.use(
			http.post("/api/api-keys/", () => {
				return HttpResponse.json(
					{ error: { code: "invalid_api_key_payload", message: "Invalid create payload" } },
					{ status: 400 },
				);
			}),
		);

		renderWithProviders(<App />);

		await user.click(await screen.findByRole("button", { name: "Create API Key" }));
		const createDialog = await screen.findByRole("dialog", { name: "Create API key" });
		await user.type(within(createDialog).getByLabelText("Name"), "Broken create");
		await user.click(within(createDialog).getByRole("button", { name: "Create" }));

		expect(await screen.findByText("Invalid create payload")).toBeInTheDocument();
	});

	it("renders the empty list state when there are no keys", async () => {
		server.use(
			http.get("/api/api-keys/", () => HttpResponse.json([])),
		);

		renderWithProviders(<App />);

		expect(await screen.findByRole("heading", { name: "APIs" })).toBeInTheDocument();
		expect(await screen.findByText("No API keys yet")).toBeInTheDocument();
	});
});
