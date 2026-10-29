import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";

import { AuthGate } from "@/features/auth/components/auth-gate";
import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

vi.mock("@/features/auth/components/totp-dialog", () => ({
  TotpDialog: () => <div>Two-factor verification</div>,
}));

function setAuthState(
  patch: Partial<ReturnType<typeof useAuthStore.getState>>,
): void {
  useAuthStore.setState({
    initialized: true,
    loading: false,
    passwordRequired: true,
    authenticated: false,
    totpRequiredOnLogin: false,
    bootstrapRequired: false,
    bootstrapTokenRequired: true,
    bootstrapTokenConfigured: false,
    authMode: "standard",
    passwordManagementEnabled: true,
    error: null,
    ...patch,
  });
}

describe("AuthGate", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    setAuthState({
      refreshSession: vi.fn().mockResolvedValue(undefined),
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows login form when unauthenticated", async () => {
    const refreshSession = vi.fn().mockResolvedValue(undefined);
    setAuthState({
      refreshSession,
      passwordRequired: true,
      authenticated: false,
      totpRequiredOnLogin: false,
    });

    render(
      <AuthGate>
        <div>Protected content</div>
      </AuthGate>,
    );

    expect(await screen.findByText("Sign in")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Administrator" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "API key reports" })).toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
  });

  it("does not mount administrator content while the initial session check is pending", () => {
    setAuthState({ initialized: false, loading: false, passwordRequired: false, refreshSession: vi.fn(() => new Promise<never>(() => {})) });
    render(<AuthGate><div>Protected content</div></AuthGate>);
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });

  it("shows children when authenticated", async () => {
    const refreshSession = vi.fn().mockResolvedValue(undefined);
    setAuthState({
      refreshSession,
      passwordRequired: true,
      authenticated: true,
      totpRequiredOnLogin: false,
    });

    render(
      <AuthGate>
        <div>Protected content</div>
      </AuthGate>,
    );

    expect(await screen.findByText("Protected content")).toBeInTheDocument();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
  });

  it("shows totp dialog when verification is pending", async () => {
    const refreshSession = vi.fn().mockResolvedValue(undefined);
    setAuthState({
      refreshSession,
      passwordRequired: true,
      authenticated: false,
      totpRequiredOnLogin: true,
    });

    render(
      <AuthGate>
        <div>Protected content</div>
      </AuthGate>,
    );

    expect(await screen.findByText("Two-factor verification")).toBeInTheDocument();
    expect(screen.queryByText("Dashboard Login")).not.toBeInTheDocument();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
  });

  it("shows reverse proxy notice when trusted header auth is required", async () => {
    const refreshSession = vi.fn().mockResolvedValue(undefined);
    setAuthState({
      refreshSession,
      passwordRequired: false,
      authenticated: false,
      totpRequiredOnLogin: false,
      authMode: "trusted_header",
    });

    render(
      <AuthGate>
        <div>Protected content</div>
      </AuthGate>,
    );

    expect(await screen.findByText("Reverse proxy authentication required")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "API key reports" })).toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
  });


  it("shows bootstrap setup screen for first-run access", async () => {
    const refreshSession = vi.fn().mockResolvedValue(undefined);
    setAuthState({
      refreshSession,
      passwordRequired: false,
      authenticated: false,
      bootstrapRequired: true,
      bootstrapTokenConfigured: true,
    });

    render(
      <AuthGate>
        <div>Protected content</div>
      </AuthGate>,
    );

    expect(await screen.findByText("Complete Dashboard Setup")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Set password" })).toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
  });

  it("keeps from and to through key sign-in and opens the owner page for that period (R29)", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const pages: URLSearchParams[] = [];
    let sessions = 0;
    server.events.on("request:start", ({ request }) => {
      const url = new URL(request.url);
      if (url.pathname === "/api/key-reports/page") pages.push(url.searchParams);
    });
    server.use(http.post("/api/key-reports/session", () => {
      sessions += 1;
      return HttpResponse.json({ authenticated: true });
    }));
    window.history.pushState({}, "", "/?from=2026-10-01&to=2026-10-28");

    renderWithProviders(<AuthGate><div>Protected content</div></AuthGate>);

    expect(await screen.findByRole("radio", { name: "API key reports" })).toBeChecked();
    await user.type(screen.getByLabelText("API key"), "synthetic-owner-key");
    await user.click(screen.getByRole("button", { name: "View reports" }));

    expect(await screen.findByRole("heading", { name: "Default key" })).toBeInTheDocument();
    server.events.removeAllListeners("request:start");
    expect(sessions).toBe(1);
    expect(window.location.search).toBe("?from=2026-10-01&to=2026-10-28");
    expect(pages[0].get("start_date")).toBe("2026-10-01");
    expect(pages[0].get("end_date")).toBe("2026-10-28");
    expect(screen.getByText(/2026-10-01 – 2026-10-28/)).toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });

  it("keeps the administrator sign-in first without a period", async () => {
    window.history.pushState({}, "", "/?view=compact");
    render(<AuthGate><div>Protected content</div></AuthGate>);
    expect(await screen.findByRole("radio", { name: "Administrator" })).toBeChecked();
  });

  it("keeps local first-run access gated without a remote warning", async () => {
    setAuthState({ passwordRequired: false, bootstrapRequired: true, bootstrapTokenRequired: false });
    render(<AuthGate><div>Protected content</div></AuthGate>);
    expect(await screen.findByText("Set Up Dashboard")).toBeInTheDocument();
    expect(screen.queryByText(/Remote setup is blocked/)).not.toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });
});
