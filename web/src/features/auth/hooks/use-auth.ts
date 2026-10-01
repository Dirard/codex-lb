import { setUnauthorizedHandler } from "@/lib/api-client";
import { create } from "zustand";

import {
  getAuthSession,
  loginPassword,
  logout as logoutRequest,
  verifyTotp as verifyTotpRequest,
} from "@/features/auth/api";
import type {
  AuthSession,
  DashboardAuthMode,
  DashboardPermission,
  DashboardRole,
} from "@/features/auth/schemas";

type AuthState = {
  passwordRequired: boolean;
  authenticated: boolean;
  totpRequiredOnLogin: boolean;
  totpConfigured: boolean;
  bootstrapRequired: boolean;
  bootstrapTokenRequired: boolean;
  bootstrapTokenConfigured: boolean;
  authMode: DashboardAuthMode;
  passwordManagementEnabled: boolean;
  passwordSessionActive: boolean;
  role: DashboardRole;
  permissions: DashboardPermission[];
  canWrite: boolean;
  loading: boolean;
  initialized: boolean;
  error: string | null;
  refreshSession: () => Promise<AuthSession>;
  login: (password: string) => Promise<AuthSession>;
  logout: () => Promise<void>;
  verifyTotp: (code: string) => Promise<AuthSession>;
  clearError: () => void;
};

function applySession(set: (next: Partial<AuthState>) => void, session: AuthSession): AuthSession {
  set({
    passwordRequired: session.passwordRequired,
    authenticated: session.authenticated,
    totpRequiredOnLogin: session.totpRequiredOnLogin,
    totpConfigured: session.totpConfigured,
    bootstrapRequired: session.bootstrapRequired ?? false,
    bootstrapTokenRequired: session.bootstrapTokenRequired ?? true,
    bootstrapTokenConfigured: session.bootstrapTokenConfigured ?? false,
    authMode: session.authMode,
    passwordManagementEnabled: session.passwordManagementEnabled,
    passwordSessionActive: session.passwordSessionActive,
    role: session.role,
    permissions: session.permissions,
    canWrite: session.permissions.includes("write"),
    initialized: true,
    error: null,
  });
  return session;
}

export const useAuthStore = create<AuthState>((set) => ({
  passwordRequired: false,
  authenticated: false,
  totpRequiredOnLogin: false,
  totpConfigured: false,
  bootstrapRequired: false,
  bootstrapTokenRequired: true,
  bootstrapTokenConfigured: false,
  authMode: "standard",
  passwordManagementEnabled: true,
  passwordSessionActive: false,
  role: "admin",
  permissions: ["read", "write"],
  canWrite: true,
  loading: false,
  initialized: false,
  error: null,
  refreshSession: async () => {
    set({ loading: true, error: null });
    try {
      const session = await getAuthSession();
      return applySession(set, session);
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Failed to refresh session",
      });
      throw error;
    } finally {
      set({ loading: false, initialized: true });
    }
  },
  login: async (password) => {
    set({ loading: true, error: null });
    try {
      const session = await loginPassword({ password });
      return applySession(set, session);
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Login failed",
      });
      throw error;
    } finally {
      set({ loading: false, initialized: true });
    }
  },
  logout: async () => {
    set({ loading: true, error: null });
    try {
      await logoutRequest();
      set({
        authenticated: false,
        totpRequiredOnLogin: false,
        bootstrapRequired: false,
        bootstrapTokenRequired: true,
        bootstrapTokenConfigured: false,
        authMode: "standard",
        passwordManagementEnabled: true,
        role: "admin",
        permissions: ["read", "write"],
        canWrite: true,
      });
      await useAuthStore.getState().refreshSession();
    } finally {
      set({ loading: false });
    }
  },
  verifyTotp: async (code) => {
    set({ loading: true, error: null });
    try {
      const session = await verifyTotpRequest({ code });
      return applySession(set, session);
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "TOTP verification failed",
      });
      throw error;
    } finally {
      set({ loading: false, initialized: true });
    }
  },
  clearError: () => {
    set({ error: null });
  },
}));

setUnauthorizedHandler(() => {
  useAuthStore.setState((state) => ({
    ...state,
    authenticated: false,
    role: "admin",
    permissions: ["read"],
    canWrite: false,
    initialized: true,
    error: null,
  }));
  void useAuthStore.getState().refreshSession().catch(() => undefined);
});
