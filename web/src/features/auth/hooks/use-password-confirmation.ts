import { useAuthStore } from "@/features/auth/hooks/use-auth";

/** Sensitive actions re-confirm the password only when it authenticates the admin. */
export function usePasswordConfirmationRequired(): boolean {
  const authMode = useAuthStore((state) => state.authMode);
  const passwordRequired = useAuthStore((state) => state.passwordRequired);
  return authMode === "standard" && passwordRequired;
}
