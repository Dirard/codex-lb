import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";

import { AuthGate } from "@/features/auth/components/auth-gate";
import { useAuthStore } from "@/features/auth/hooks/use-auth";

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


  it("shows bootstrap setup screen for remote first-run access", async () => {
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

    expect(await screen.findByText("Complete Remote Setup")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Set password" })).toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
  });

  it("keeps local first-run access gated without a remote warning", async () => {
    setAuthState({ passwordRequired: false, bootstrapRequired: true, bootstrapTokenRequired: false });
    render(<AuthGate><div>Protected content</div></AuthGate>);
    expect(await screen.findByText("Set Up Dashboard")).toBeInTheDocument();
    expect(screen.queryByText(/Remote setup is blocked/)).not.toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });
});
