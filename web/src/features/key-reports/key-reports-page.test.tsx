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

function mockReports(handler: (options?: RequestInit, key?: string) => Response | Promise<Response>, session: {
  login?: (options?: RequestInit) => Response | Promise<Response>;
  restore?: () => Response | Promise<Response>;
  logout?: () => Response | Promise<Response>;
} = {}) {
  let key = "";
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
    if (String(url) === "/api/dashboard-auth/session") {
      return response({ authenticated: false, passwordRequired: true, totpRequiredOnLogin: false, totpConfigured: false });
    }
    if (String(url) === "/api/key-reports/session") {
      if (options?.method === "POST") {
        const result = await (session.login?.(options) ?? response({ authenticated: true }));
        if (result.ok) key = new Headers(options?.headers).get("Authorization")!.slice(7);
        return result;
      }
      if (options?.method === "DELETE") {
        const result = await (session.logout?.() ?? response({ authenticated: false }));
        if (result.ok) key = "";
        return result;
      }
      return session.restore?.() ?? response({ authenticated: key !== "" });
    }
    if (!String(url).startsWith("/api/key-reports/reports?")) throw new Error("Unexpected administrator data request");
    return handler(options, key);
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
    mockReports((_options, key) => response(key === "synthetic-key-a"
      ? data : report("only-key-b-model", 0)));
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(screen.queryByRole("heading", { name: "Runtime updates" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Check for updates" })).not.toBeInTheDocument();
    const personal = await screen.findByRole("region", { name: "Personal limits" });
    expect(within(personal).getByText("$2.50 / $10.00")).toBeInTheDocument();
    const group = screen.getByText("Group keys: Team A").closest("details")!;
    expect(group).not.toHaveAttribute("open");
    await user.click(screen.getByText("Group keys: Team A"));
    expect(screen.queryByRole("region", { name: "Shared limits" })).not.toBeInTheDocument();
    const credits = screen.getByRole("region", { name: "Extra credits" });
    expect(within(credits).getByText("≥ 35.75")).toBeInTheDocument();
    expect(within(credits).getByTitle("Known credits: 2 of 3 subscription accounts")).toHaveTextContent("Partial balance");
    expect(within(group).getByText("Alice (you)")).toBeInTheDocument();
    expect(within(group).getByText("25%")).toBeInTheDocument();
    expect(within(group).getByText("72%")).toBeInTheDocument();
    expect(within(group).getByText("Disabled")).toBeInTheDocument();
    expect(within(group).getByText("Expired")).toBeInTheDocument();
    expect(within(group).getByText("No limits configured")).toBeInTheDocument();
    expect(within(group).queryByRole("button", { name: /edit|delete|reset/i })).not.toBeInTheDocument();
    expect(within(group).queryByRole("link")).not.toBeInTheDocument();
    data.group.keys[1].limits[0].currentValue = 12_000_000;
    data.group.accountQuota!.creditsUnlimited = true;
    data.limits = [];
    data.group.keys[0].limits = [];
    await user.click(screen.getByRole("button", { name: "Apply / refresh" }));
    expect(await within(group).findByText("120%")).toBeInTheDocument();
    const accounts = screen.getByRole("region", { name: "Shared limits" });
    expect(within(accounts).getByText("42%")).toBeInTheDocument();
    expect(within(accounts).getByTitle("Subscription accounts: 2")).toBeInTheDocument();
    expect(within(accounts).queryByText("5-hour usage")).not.toBeInTheDocument();
    expect(within(credits).getByText("Unlimited")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await signIn("synthetic-key-b");
    expect(await screen.findByText("only-key-b-model")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Personal limits" })).toHaveTextContent("Unlimited");
    expect(screen.queryByRole("region", { name: "Shared limits" })).not.toBeInTheDocument();
    expect(screen.queryByText("$2.50 / $10.00")).not.toBeInTheDocument();
    expect(screen.queryByText("Bob")).not.toBeInTheDocument();
  });

  it("offers both methods and sends the key only once to create the session", async () => {
    const fetchMock = mockReports(() => response(report("only-key-a-model", 1)));
    const { queryClient } = openPage();
    expect(await screen.findByLabelText("Password")).toHaveAttribute("type", "password");
    expect(screen.getByRole("radio", { name: "Administrator" })).toBeChecked();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await signIn("synthetic-key-a");
    expect(await screen.findByText("only-key-a-model")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Accounts" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create key" })).not.toBeInTheDocument();
    for (const [url, options] of fetchMock.mock.calls) {
      expect(String(url)).not.toContain("synthetic-key-a");
      if (options?.method === "POST") {
        expect(url).toBe("/api/key-reports/session");
        expect(new Headers(options.headers).get("Authorization")).toBe("Bearer synthetic-key-a");
      } else {
        expect(new Headers(options?.headers).has("Authorization")).toBe(false);
      }
      if (String(url).startsWith("/api/key-reports/")) {
        expect(options).toMatchObject({ credentials: "same-origin", cache: "no-store" });
      }
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
    mockReports(() => response(report("must-not-show", 0)), {
      login: () => response({ error: { code: "invalid_api_key", message: "Invalid API key" } }, 401),
    });
    openPage();
    await signIn("synthetic-bad-key");
    expect(await screen.findByRole("alert")).toHaveTextContent("The API key is invalid, disabled or expired.");
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View reports" })).toBeEnabled();
  });

  it("cancels a pending read on logout and never reuses another key's report", async () => {
    let blockA = false;
    let aborted = false;
    mockReports((options, key) => {
      if (key === "synthetic-key-b") return Promise.resolve(response(report("only-key-b-model", 2)));
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

  it("clears abandoned drafts and prevents switching methods while session creation is pending", async () => {
    let complete: (value: Response) => void = () => { throw new Error("No login request"); };
    mockReports(() => response(report("accepted-key-model", 9)), {
      login: () => new Promise<Response>((resolve) => { complete = resolve; }),
    });
    openPage();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Password"), "synthetic-admin-draft");
    await user.click(screen.getByRole("radio", { name: "API key reports" }));
    await user.type(screen.getByLabelText("API key"), "synthetic-abandoned-key");
    await user.click(screen.getByRole("radio", { name: "Administrator" }));
    expect(screen.getByLabelText("Password")).toHaveValue("");
    await user.click(screen.getByRole("radio", { name: "API key reports" }));
    expect(screen.getByLabelText("API key")).toHaveValue("");
    await signIn("synthetic-pending-key");
    expect(screen.getByRole("radio", { name: "Administrator" })).toBeDisabled();
    await act(async () => complete(response({ authenticated: true })));
    expect(await screen.findByText("accepted-key-model")).toBeInTheDocument();
  });

  it("restores reports after remount without another key submission, then stays signed out after logout", async () => {
    const fetchMock = mockReports(() => response(report("persistent-model", 1)));
    const first = openPage();
    const user = await signIn("synthetic-persistent-key");
    expect(await screen.findByText("persistent-model")).toBeInTheDocument();
    first.unmount();
    useAuthStore.setState(initialAuthState, true);
    const reloaded = openPage();
    expect(await screen.findByText("persistent-model")).toBeInTheDocument();
    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByLabelText("API key")).toHaveValue("");
    reloaded.unmount();
    useAuthStore.setState(initialAuthState, true);
    openPage();
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByText("persistent-model")).not.toBeInTheDocument();
  });

  it("retries a failed session restoration without requiring the key or mounting admin data", async () => {
    let failed = true;
    mockReports(() => response(report("recovered-session-model", 1)), {
      restore: () => failed ? response({ error: { code: "server_error", message: "Unavailable" } }, 500) : response({ authenticated: true }),
    });
    openPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not restore your session");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    failed = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("recovered-session-model")).toBeInTheDocument();
  });

  it.each(["network", "server"])("does not log out after a %s report failure", async (kind) => {
    let failed = false;
    mockReports(() => {
      if (!failed) return response(report("recoverable-report", 1));
      if (kind === "network") throw new TypeError("Network unavailable");
      return response({ error: { code: "server_error", message: "Unavailable" } }, 500);
    });
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByText("recoverable-report")).toBeInTheDocument();
    failed = true;
    await user.click(screen.getByRole("button", { name: "Apply / refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load reports");
    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();
    failed = false;
    await user.click(screen.getByRole("button", { name: "Apply / refresh" }));
    expect(await screen.findByText("recoverable-report")).toBeInTheDocument();
  });

  it("does not claim successful logout when cookie deletion fails", async () => {
    let failed = true;
    mockReports(() => response(report("logout-retry-model", 1)), {
      logout: () => failed ? response({ error: { code: "server_error", message: "Unavailable" } }, 500) : response({ authenticated: false }),
    });
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByText("logout-retry-model")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not sign out");
    expect(screen.getByText("logout-retry-model")).toBeInTheDocument();
    failed = false;
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByLabelText("API key")).toHaveValue("");
  });
});
