import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createApiKey } from "@/test/mocks/factories";
import { renderWithProviders } from "@/test/utils";

import { ApisPage } from "./apis-page";

const hookMocks = vi.hoisted(() => ({
	useApiKeys: vi.fn(),
}));

vi.mock("@/features/apis/hooks/use-apis", () => hookMocks);

type MutationMock = {
	isPending: boolean;
	error: Error | null;
	mutateAsync: ReturnType<typeof vi.fn>;
};

type QueryMock<T> = {
	data: T | undefined;
	error: Error | null;
	isPending: boolean;
	isFetching: boolean;
	refetch: ReturnType<typeof vi.fn>;
};

function createMutationMock(): MutationMock {
	return {
		isPending: false,
		error: null,
		mutateAsync: vi.fn(),
	};
}

function createQueryMock<T>(data: T | undefined): QueryMock<T> {
	return {
		data,
		error: null,
		isPending: false,
		isFetching: false,
		refetch: vi.fn(),
	};
}

function renderApisPage({
	apiKeys = [createApiKey()],
	apiKeysQuery = createQueryMock(apiKeys),
	createMutation = createMutationMock(),
	updateMutation = createMutationMock(),
	deleteMutation = createMutationMock(),
	regenerateMutation = createMutationMock(),
}: {
	apiKeys?: ReturnType<typeof createApiKey>[];
	apiKeysQuery?: QueryMock<ReturnType<typeof createApiKey>[]>;
	createMutation?: MutationMock;
	updateMutation?: MutationMock;
	deleteMutation?: MutationMock;
	regenerateMutation?: MutationMock;
} = {}) {
	hookMocks.useApiKeys.mockReturnValue({
		apiKeysQuery,
		createMutation,
		updateMutation,
		deleteMutation,
		regenerateMutation,
	});

	return renderWithProviders(<ApisPage />);
}

afterEach(() => {
	vi.clearAllMocks();
});

describe("ApisPage", () => {
	it("keeps the create dialog open when creation fails", async () => {
		const user = userEvent.setup();
		const createMutation = createMutationMock();
		createMutation.mutateAsync.mockRejectedValue(new Error("boom create"));

		renderApisPage({ createMutation });

		await user.click(screen.getByRole("button", { name: "Create API Key" }));
		const dialog = await screen.findByRole("dialog", { name: "Create API key" });
		const nameInput = within(dialog).getByLabelText("Name");

		await user.type(nameInput, "Broken key");
		await user.click(within(dialog).getByRole("button", { name: "Create" }));

		await waitFor(() => {
			expect(createMutation.mutateAsync).toHaveBeenCalledTimes(1);
		});
		expect(screen.getByRole("dialog", { name: "Create API key" })).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("Broken key");
	});

	it("opens the key page when a key is selected, without a detail pane", async () => {
		const user = userEvent.setup();
		renderApisPage({ apiKeys: [createApiKey(), createApiKey({ id: "key_2", name: "Secondary key" })] });

		expect(screen.queryByTestId("api-key-info")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Actions" })).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /Secondary key/ }));

		await waitFor(() => expect(window.location.pathname).toBe("/keys/key_2"));
	});

	it("shows a retry state when the initial API key query fails", async () => {
		const user = userEvent.setup();
		const apiKeysQuery = createQueryMock<ReturnType<typeof createApiKey>[]>(undefined);
		apiKeysQuery.error = new Error("boom list");

		renderApisPage({ apiKeys: [], apiKeysQuery });

		expect(screen.getByText("boom list")).toBeInTheDocument();
		const retryButton = screen.getByRole("button", { name: "Retry" });
		await user.click(retryButton);

		expect(apiKeysQuery.refetch).toHaveBeenCalledTimes(1);
		expect(screen.queryByText("Create API Key")).not.toBeInTheDocument();
	});

	it("labels the key limit bar as API Limit without pooled account bars", () => {
		renderApisPage({ apiKeys: [createApiKey()] });

		expect(screen.queryByText("Pooled 5h")).not.toBeInTheDocument();
		expect(screen.queryByText("Pooled Weekly")).not.toBeInTheDocument();
		expect(screen.getByText("API Limit")).toBeInTheDocument();
	});

	it("renders the overview section for the full API key list", () => {
		renderApisPage({
			apiKeys: [
				createApiKey({
					usageSummary: {
						requestCount: 300,
						totalTokens: 80_000,
						cachedInputTokens: 12_000,
						totalCostUsd: 2.5,
					},
				}),
				createApiKey({
					id: "key_2",
					name: "Secondary key",
					keyPrefix: "sk-secondary",
					usageSummary: {
						requestCount: 120,
						totalTokens: 20_000,
						cachedInputTokens: 2_000,
						totalCostUsd: 1.0,
					},
				}),
			],
		});

		expect(screen.getByText("Overview")).toBeInTheDocument();
		expect(screen.getByText("Lifetime Cost by API Key")).toBeInTheDocument();
		expect(screen.getByText("Lifetime Tokens by API Key")).toBeInTheDocument();
		expect(
			within(screen.getByTestId("api-keys-overview-cost-panel")).getByText("Secondary key"),
		).toBeInTheDocument();
	});
});
