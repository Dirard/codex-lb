import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "@/App";
import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { createApiKey } from "@/test/mocks/factories";
import { createOwnerKeyPage, createKeyRequestRows, pageKeyRequests } from "@/test/mocks/key-page-factories";
import { renderWithProviders } from "@/test/utils";

function ownerPage(name: string, overrides: Parameters<typeof createOwnerKeyPage>[1] = {}) {
  return createOwnerKeyPage(createApiKey({ id: `key_${name}`, name }), overrides);
}

function response(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
}

const initialAuthState = useAuthStore.getState();

/**
 * The owner portal against a fetch stub: the dashboard session is signed out,
 * the report session follows the key it was created with, and any request
 * outside /api/key-reports/ fails the test.
 */
function mockPortal(page: (options?: RequestInit, key?: string) => Response | Promise<Response>, session: {
  login?: (options?: RequestInit) => Response | Promise<Response>;
  restore?: () => Response | Promise<Response>;
  logout?: () => Response | Promise<Response>;
} = {}) {
  let key = "";
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
    const path = String(url);
    if (path === "/api/dashboard-auth/session") {
      return response({ authenticated: false, passwordRequired: true, totpRequiredOnLogin: false, totpConfigured: false });
    }
    if (path === "/api/key-reports/session") {
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
    if (path.startsWith("/api/key-reports/requests?")) {
      return response(pageKeyRequests(createKeyRequestRows(60, "key_owner"), { cursor: null, limit: 50, errorsOnly: false }, "owner"));
    }
    if (!path.startsWith("/api/key-reports/page?")) throw new Error(`Unexpected administrator data request ${path}`);
    return page(options, key);
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

describe("key owner page", () => {
  it("shows the owner key page with the group mini-list and no administrator access", async () => {
    const limit = { id: 1, limitType: "cost_usd" as const, limitWindow: "weekly" as const,
      maxValue: 10_000_000, currentValue: 2_500_000, modelFilter: null, resetAt: "2099-01-01T00:00:00Z" };
    const data = ownerPage("Alice", {
      overrides: {
        group: { id: "grp_a", name: "Team A", keys: [
          { id: "me", name: "Alice", isCurrent: true, isActive: true, expiresAt: null, limits: [limit] },
          { id: "peer", name: "Bob", isCurrent: false, isActive: false, expiresAt: null, limits: [{ ...limit, id: 2, currentValue: 7_200_000 }] },
        ] },
      },
    });
    mockPortal((_options, key) => response(key === "synthetic-key-a" ? data : ownerPage("Only key B")));
    openPage();
    const user = await signIn("synthetic-key-a");

    expect(await screen.findByRole("heading", { name: "Alice" })).toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy owner link" })).not.toBeInTheDocument();
    const group = screen.getByText("Group keys: Team A").closest("details")!;
    await user.click(screen.getByText("Group keys: Team A"));
    expect(within(group).getByText("Alice (you)")).toBeInTheDocument();
    expect(within(group).getByText("72%")).toBeInTheDocument();
    expect(within(group).getByText("Disabled")).toBeInTheDocument();
    expect(within(group).queryByRole("button", { name: /edit|delete|reset/i })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await signIn("synthetic-key-b");
    expect(await screen.findByRole("heading", { name: "Only key B" })).toBeInTheDocument();
    expect(screen.queryByText("Bob")).not.toBeInTheDocument();
  });

  it("offers both methods and sends the key only once to create the session", async () => {
    const fetchMock = mockPortal(() => response(ownerPage("Only key A")));
    const { queryClient } = openPage();
    expect(await screen.findByLabelText("Password")).toHaveAttribute("type", "password");
    expect(screen.getByRole("radio", { name: "Administrator" })).toBeChecked();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await signIn("synthetic-key-a");
    expect(await screen.findByRole("heading", { name: "Only key A" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Accounts" })).not.toBeInTheDocument();
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
    mockPortal(() => response(ownerPage("Unused")));
    openPage("/key-reports");
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "API key reports" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  it("keeps invalid keys on the sign-in form without exposing the page", async () => {
    mockPortal(() => response(ownerPage("Must not show")), {
      login: () => response({ error: { code: "invalid_api_key", message: "Invalid API key" } }, 401),
    });
    openPage();
    await signIn("synthetic-bad-key");
    expect(await screen.findByRole("alert")).toHaveTextContent("The API key is invalid, disabled or expired.");
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View reports" })).toBeEnabled();
  });

  it("cancels a pending read on sign-out and never reuses another key's page", async () => {
    let blockA = false;
    let aborted = false;
    mockPortal((options, key) => {
      if (key === "synthetic-key-b") return Promise.resolve(response(ownerPage("Only key B")));
      if (!blockA) return Promise.resolve(response(ownerPage("Only key A")));
      return new Promise((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () => { aborted = true; reject(new DOMException("Aborted", "AbortError")); }, { once: true });
      });
    });
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByRole("heading", { name: "Only key A" })).toBeInTheDocument();
    blockA = true;
    await user.click(screen.getByRole("button", { name: "Refresh key page" }));
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(aborted).toBe(true));
    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect(screen.queryByRole("heading", { name: "Only key A" })).not.toBeInTheDocument();
    await signIn("synthetic-key-b");
    expect(await screen.findByRole("heading", { name: "Only key B" })).toBeInTheDocument();
    expect(screen.queryByText("Only key A")).not.toBeInTheDocument();
  });

  it("hides loaded data and returns to sign-in when the key is revoked", async () => {
    let valid = true;
    mockPortal(() => Promise.resolve(valid
      ? response(ownerPage("Only key A"))
      : response({ error: { code: "invalid_api_key", message: "Invalid API key" } }, 401)));
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByRole("heading", { name: "Only key A" })).toBeInTheDocument();
    valid = false;
    await user.click(screen.getByRole("button", { name: "Refresh key page" }));
    expect(await screen.findByLabelText("API key")).toHaveValue("");
    expect(screen.queryByText("Only key A")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("The API key is invalid, disabled or expired.");
  });

  it("clears abandoned drafts and prevents switching methods while session creation is pending", async () => {
    let complete: (value: Response) => void = () => { throw new Error("No login request"); };
    mockPortal(() => response(ownerPage("Accepted key")), {
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
    expect(await screen.findByRole("heading", { name: "Accepted key" })).toBeInTheDocument();
  });

  it("restores the page after remount without another key submission, then stays signed out after sign-out", async () => {
    const fetchMock = mockPortal(() => response(ownerPage("Persistent key")));
    const first = openPage();
    const user = await signIn("synthetic-persistent-key");
    expect(await screen.findByRole("heading", { name: "Persistent key" })).toBeInTheDocument();
    first.unmount();
    useAuthStore.setState(initialAuthState, true);
    const reloaded = openPage();
    expect(await screen.findByRole("heading", { name: "Persistent key" })).toBeInTheDocument();
    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByLabelText("API key")).toHaveValue("");
    reloaded.unmount();
    useAuthStore.setState(initialAuthState, true);
    openPage();
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByText("Persistent key")).not.toBeInTheDocument();
  });

  it("retries a failed session restoration without requiring the key or mounting admin data", async () => {
    let failed = true;
    mockPortal(() => response(ownerPage("Recovered session")), {
      restore: () => failed ? response({ error: { code: "server_error", message: "Unavailable" } }, 500) : response({ authenticated: true }),
    });
    openPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not restore your session");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    failed = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Recovered session" })).toBeInTheDocument();
  });

  it.each(["network", "server"])("does not sign out after a %s failure", async (kind) => {
    let failed = false;
    mockPortal(() => {
      if (!failed) return response(ownerPage("Recoverable key"));
      if (kind === "network") throw new TypeError("Network unavailable");
      return response({ error: { code: "server_error", message: "Unavailable" } }, 500);
    });
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByRole("heading", { name: "Recoverable key" })).toBeInTheDocument();
    failed = true;
    await user.click(screen.getByRole("button", { name: "Refresh key page" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Recoverable key" })).toBeInTheDocument();
    failed = false;
    await user.click(within(alert).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });

  it("does not claim a successful sign-out when cookie deletion fails", async () => {
    let failed = true;
    mockPortal(() => response(ownerPage("Sign-out retry")), {
      logout: () => failed ? response({ error: { code: "server_error", message: "Unavailable" } }, 500) : response({ authenticated: false }),
    });
    openPage();
    const user = await signIn("synthetic-key-a");
    expect(await screen.findByRole("heading", { name: "Sign-out retry" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not sign out");
    expect(screen.getByRole("heading", { name: "Sign-out retry" })).toBeInTheDocument();
    failed = false;
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByLabelText("API key")).toHaveValue("");
  });
});
