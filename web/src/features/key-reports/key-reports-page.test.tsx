import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "@/App";
import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { renderWithProviders } from "@/test/utils";
import type { KeyReport } from "./api";

vi.mock("@/features/reports/components/cost-per-day-chart", () => ({ CostPerDayChart: () => <div>Cost chart</div> }));
vi.mock("@/features/reports/components/tokens-per-day-chart", () => ({ TokensPerDayChart: () => <div>Token chart</div> }));
vi.mock("@/features/reports/components/model-distribution-donut", () => ({
  ModelDistributionDonut: ({ data }: { data: KeyReport["byModel"] }) => <div>{data.map((entry) => <span key={entry.model}>{entry.model}</span>)}</div>,
}));

function report(model: string, cost: number): KeyReport {
  return {
    summary: { totalCostUsd: cost, totalInputTokens: 10, totalOutputTokens: 5, totalReasoningTokens: 0,
      reasoningUsageKnownRequests: 0, totalCachedTokens: 0, totalRequests: 1, totalCancelled: 0, totalErrors: 0,
      totalConversations: 1, activeAccounts: 1, avgCostPerDay: cost / 7, avgRequestsPerDay: 1 / 7 },
    comparison: { canCompare: false, previous: { totalCostUsd: 0, totalTokens: 0, totalRequests: 0 } },
    daily: [], byModel: [{ model, costUsd: cost, requests: 1, percentage: 100 }], byUseragent: [],
    limits: [], group: null,
  };
}

function response(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
}

const initialAuthState = useAuthStore.getState();

function mockReports(handler: (options?: RequestInit) => Response | Promise<Response>) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((url, options) => {
    if (String(url) === "/api/dashboard-auth/session") {
      return Promise.resolve(response({ authenticated: false, passwordRequired: true, totpRequiredOnLogin: false, totpConfigured: false }));
    }
    if (!String(url).startsWith("/v1/usage/reports?")) throw new Error("Unexpected administrator data request");
    return Promise.resolve(handler(options));
  });
}

function openPage(path = "/") {
  window.history.pushState({}, "", path);
  return renderWithProviders(<App />);
}

async function signIn(key: string) {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "API key reports" }));
  await user.type(await screen.findByLabelText("API key"), key);
  await user.click(screen.getByRole("button", { name: "View reports" }));
  return user;
}

beforeEach(() => useAuthStore.setState(initialAuthState, true));

afterEach(() => {
  vi.restoreAllMocks();
  window.history.pushState({}, "", "/");
});

describe("key report portal", () => {
  it("reuses personal limit details and group mini-bars without administrator access", async () => {
    const data = report("own-model", 1);
    const limit = { id: 1, limitType: "cost_usd" as const, limitWindow: "weekly" as const,
      maxValue: 10_000_000, currentValue: 2_500_000, modelFilter: null, resetAt: "2099-01-01T00:00:00Z" };
    data.limits = [limit];
    data.group = { name: "Team A", accountQuota: { accountCount: 3,
      purchasedCredits: 35.75, creditsUnlimited: false, creditsKnownAccountCount: 2,
      windows: [{ window: "secondary", usedPercent: 42, accountCount: 2 }] }, keys: [
      { id: "me", name: "Alice", isCurrent: true, isActive: true, expiresAt: null, limits: [limit] },
      { id: "peer", name: "Bob", isCurrent: false, isActive: false, expiresAt: null,
        limits: [{ ...limit, id: 2, currentValue: 7_200_000 }] },
      { id: "no-limits", name: "Carol", isCurrent: false, isActive: true, expiresAt: "2000-01-01T00:00:00Z", limits: [] },
    ] };
    mockReports((options) => response(new Headers(options?.headers).get("Authorization") === "Bearer synthetic-key-a"
      ? data : report("only-key-b-model", 0)));
    openPage();
    const user = await signIn("synthetic-key-a");
    const personal = await screen.findByRole("region", { name: "Your limits" });
    expect(within(personal).getByText("$2.50 / $10.00")).toBeInTheDocument();
    const group = screen.getByRole("region", { name: "Group keys: Team A" });
    const accounts = within(group).getByRole("region", { name: "Group accounts: subscription usage" });
    expect(within(accounts).getByText("42%")).toBeInTheDocument();
    expect(within(accounts).getByText("Purchased credits remaining")).toBeInTheDocument();
    expect(within(accounts).getByText("35.75")).toBeInTheDocument();
    expect(within(accounts).getByText("Known credits: 2 of 3 subscription accounts")).toBeInTheDocument();
    expect(within(accounts).getByText("Known quota: 2 of 3 subscription accounts")).toBeInTheDocument();
    expect(within(accounts).queryByText("5-hour usage")).not.toBeInTheDocument();
    expect(within(group).getByText("Alice (you)")).toBeInTheDocument();
    expect(within(group).getByText("25%")).toBeInTheDocument();
    expect(within(group).getByText("72%")).toBeInTheDocument();
    expect(within(group).getByText("Disabled")).toBeInTheDocument();
    expect(within(group).getByText("Expired")).toBeInTheDocument();
    expect(within(group).getByText("No limits configured")).toBeInTheDocument();
    expect(within(group).queryByRole("button")).not.toBeInTheDocument();
    expect(within(group).queryByRole("link")).not.toBeInTheDocument();
    data.group.keys[1].limits[0].currentValue = 12_000_000;
    data.group.accountQuota!.creditsUnlimited = true;
    await user.click(screen.getByRole("button", { name: "Apply / refresh" }));
    expect(await within(group).findByText("120%")).toBeInTheDocument();
    expect(within(accounts).getByText("Unlimited")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await signIn("synthetic-key-b");
    expect(await screen.findByText("only-key-b-model")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Your limits" })).not.toBeInTheDocument();
    expect(screen.queryByText("Bob")).not.toBeInTheDocument();
  });

  it("offers both methods on the common screen and sends the key only in the report header", async () => {
    const fetchMock = mockReports(() => response(report("only-key-a-model", 1)));
    const { queryClient } = openPage();
    expect(await screen.findByLabelText("Password")).toHaveAttribute("type", "password");
    expect(screen.getByRole("radio", { name: "Administrator" })).toBeChecked();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await signIn("synthetic-key-a");
    expect(await screen.findByText("only-key-a-model")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Accounts" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create key" })).not.toBeInTheDocument();
    for (const [url, options] of fetchMock.mock.calls) {
      if (String(url) === "/api/dashboard-auth/session") {
        expect(new Headers(options?.headers).has("Authorization")).toBe(false);
        continue;
      }
      expect(String(url).startsWith("/v1/usage/reports?")).toBe(true);
      expect(String(url)).not.toContain("synthetic-key-a");
      expect(options).toMatchObject({ method: "GET", credentials: "omit", cache: "no-store" });
      expect(new Headers(options?.headers).get("Authorization")).toBe("Bearer synthetic-key-a");
    }
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
    expect(JSON.stringify(localStorage)).not.toContain("synthetic-key-a");
    expect(JSON.stringify(sessionStorage)).not.toContain("synthetic-key-a");
    expect(window.location.href).not.toContain("synthetic-key-a");
    expect(useAuthStore.getState().authenticated).toBe(false);
  });

  it("redirects the old key-report entry to the common sign-in screen", async () => {
    mockReports(() => response(report("unused-model", 0)));
    openPage("/key-reports");
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "API key reports" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  it("keeps invalid keys on the sign-in form without exposing reports", async () => {
    mockReports(() => response({ error: { code: "invalid_api_key", message: "Invalid API key" } }, 401));
    openPage();
    await signIn("synthetic-bad-key");
    expect(await screen.findByRole("alert")).toHaveTextContent("The API key is invalid, disabled or expired.");
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View reports" })).toBeEnabled();
  });

  it("cancels a pending read on logout and never reuses another key's report", async () => {
    let blockA = false;
    let aborted = false;
    mockReports((options) => {
      const key = new Headers(options?.headers).get("Authorization");
      if (key === "Bearer synthetic-key-b") return Promise.resolve(response(report("only-key-b-model", 2)));
      if (!blockA) return Promise.resolve(response(report("only-key-a-model", 1)));
      return new Promise((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () => { aborted = true; reject(new DOMException("Aborted", "AbortError")); }, { once: true });
      });
    });
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByText("only-key-a-model")).toBeInTheDocument();
    blockA = true;
    await user.click(screen.getByRole("button", { name: "Apply / refresh" }));
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(aborted).toBe(true));
    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect(screen.queryByText("only-key-a-model")).not.toBeInTheDocument();
    await signIn("synthetic-key-b");
    expect(await screen.findByText("only-key-b-model")).toBeInTheDocument();
    expect(screen.queryByText("only-key-a-model")).not.toBeInTheDocument();
  });

  it("hides loaded data and returns to sign-in when the key is revoked", async () => {
    let valid = true;
    mockReports(() => Promise.resolve(valid
      ? response(report("only-key-a-model", 1))
      : response({ error: { code: "invalid_api_key", message: "Invalid API key" } }, 401)));
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByText("only-key-a-model")).toBeInTheDocument();
    valid = false;
    await user.click(screen.getByRole("button", { name: "Apply / refresh" }));
    expect(await screen.findByLabelText("API key")).toHaveValue("");
    expect(screen.queryByText("only-key-a-model")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("The API key is invalid, disabled or expired.");
  });

  it("cancels abandoned key login and clears both credential drafts when switching methods", async () => {
    let aborted = false;
    let complete: (value: Response) => void = () => { throw new Error("No login request"); };
    mockReports((options) => new Promise<Response>((resolve) => {
      complete = resolve;
      options?.signal?.addEventListener("abort", () => { aborted = true; }, { once: true });
    }));
    openPage();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Password"), "synthetic-admin-draft");
    await signIn("synthetic-pending-key");
    await user.click(screen.getByRole("radio", { name: "Administrator" }));
    expect(aborted).toBe(true);
    expect(screen.getByLabelText("Password")).toHaveValue("");
    await act(async () => complete(response(report("abandoned-key-model", 9))));
    expect(screen.queryByText("abandoned-key-model")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "API key reports" }));
    expect(screen.getByLabelText("API key")).toHaveValue("");
  });
});
