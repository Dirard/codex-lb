import { beforeEach, describe, expect, it, vi } from "vitest";

let registeredUnauthorizedHandler: (() => void) | null = null;
const getAuthSession = vi.fn();

vi.mock("@/features/auth/api", () => ({
  getAuthSession,
  loginPassword: vi.fn(),
  logout: vi.fn(),
  verifyTotp: vi.fn(),
}));

vi.mock("@/lib/api-client", () => ({
  ApiError: class ApiError extends Error {
    status: number;
    code: string;

    constructor({ message, status, code }: { message: string; status: number; code: string }) {
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.code = code;
    }
  },
  setUnauthorizedHandler: (handler: (() => void) | null) => {
    registeredUnauthorizedHandler = handler;
  },
}));

describe("useAuthStore unauthorized handler", () => {
  beforeEach(() => {
    getAuthSession.mockReset();
    vi.clearAllMocks();
  });

  it("refreshes server auth state on 401 handling", async () => {
    const { useAuthStore } = await import("@/features/auth/hooks/use-auth");
    getAuthSession.mockResolvedValue({
      passwordRequired: false,
      authenticated: false,
      totpRequiredOnLogin: false,
      totpConfigured: false,
      bootstrapRequired: true,
      bootstrapTokenConfigured: true,
      authMode: "standard",
      passwordManagementEnabled: true,
      passwordSessionActive: false,
      role: "admin",
      permissions: ["read"],
    });

    useAuthStore.setState({
      authenticated: true,
      initialized: true,
      bootstrapRequired: false,
      bootstrapTokenConfigured: false,
      error: "boom",
    });

    expect(registeredUnauthorizedHandler).not.toBeNull();
    registeredUnauthorizedHandler?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(getAuthSession).toHaveBeenCalledTimes(1);

    const next = useAuthStore.getState();
    expect(next.authenticated).toBe(false);
    expect(next.initialized).toBe(true);
    expect(next.error).toBeNull();
    expect(next.bootstrapRequired).toBe(true);
    expect(next.bootstrapTokenConfigured).toBe(true);
  });

  it("clears write permissions on 401 handling", async () => {
    const { useAuthStore } = await import("@/features/auth/hooks/use-auth");

    useAuthStore.setState({
      authenticated: true,
      initialized: true,
      role: "admin",
      permissions: ["read", "write"],
      canWrite: true,
    });

    expect(registeredUnauthorizedHandler).not.toBeNull();
    registeredUnauthorizedHandler?.();

    const next = useAuthStore.getState();
    expect(next.authenticated).toBe(false);
    expect(next.role).toBe("admin");
    expect(next.permissions).toEqual(["read"]);
    expect(next.canWrite).toBe(false);
  });
});
