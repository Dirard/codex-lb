import { lazy, Suspense, useEffect, useState } from "react";
import type { PropsWithChildren } from "react";
import { useTranslation } from "react-i18next";

import { CodexLogo } from "@/components/brand/codex-logo";
import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { SpinnerBlock } from "@/components/ui/spinner";
import { BootstrapSetupScreen } from "@/features/auth/components/bootstrap-setup-screen";
import { LoginForm } from "@/features/auth/components/login-form";
import { KeyLoginForm } from "@/features/auth/components/key-login-form";
import { TotpDialog } from "@/features/auth/components/totp-dialog";
import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { getKeyReportSession } from "@/features/key-reports/api";

const KeyReportsPage = lazy(() =>
  import("@/features/key-reports/key-reports-page").then((m) => ({ default: m.KeyReportsPage })),
);

export function AuthGate({ children }: PropsWithChildren) {
  const { t } = useTranslation();
  const [loginMethod, setLoginMethod] = useState<"admin" | "key">("admin");
  const [keySession, setKeySession] = useState(false);
  const [keyLoginBusy, setKeyLoginBusy] = useState(false);
  const [restoration, setRestoration] = useState<"loading" | "ready" | "error">("loading");
  const [restoreAttempt, setRestoreAttempt] = useState(0);
  const [keyLoginError, setKeyLoginError] = useState<string | null>(null);
  const refreshSessionStable = useAuthStore((state) => state.refreshSession);
  const initialized = useAuthStore((state) => state.initialized);
  const loading = useAuthStore((state) => state.loading);
  const passwordRequired = useAuthStore((state) => state.passwordRequired);
  const authenticated = useAuthStore((state) => state.authenticated);
  const bootstrapRequired = useAuthStore((state) => state.bootstrapRequired);
  const totpRequiredOnLogin = useAuthStore((state) => state.totpRequiredOnLogin);
  const authMode = useAuthStore((state) => state.authMode);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([refreshSessionStable(), getKeyReportSession(controller.signal)]).then(([, report]) => {
      if (controller.signal.aborted) return;
      setKeySession(report.authenticated && !useAuthStore.getState().authenticated);
      setRestoration("ready");
    }).catch(() => {
      if (!controller.signal.aborted) setRestoration("error");
    });
    return () => controller.abort();
  }, [refreshSessionStable, restoreAttempt]);

  if (restoration === "error") {
    return <div className="flex min-h-screen flex-col items-center justify-center gap-4 p-4">
      <div role="alert"><AlertMessage variant="error">{t("keyReports.sessionLoadFailed")}</AlertMessage></div>
      <Button onClick={() => { setRestoration("loading"); setRestoreAttempt((value) => value + 1); }}>{t("keyReports.retry")}</Button>
    </div>;
  }

  if (!initialized || restoration === "loading") {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <SpinnerBlock />
      </div>
    );
  }

  // Key authentication selects only its report surface, never administrator children.
  if (keySession) {
    return <Suspense fallback={<SpinnerBlock />}><KeyReportsPage onExit={(message) => {
      setKeySession(false);
      setKeyLoginBusy(false);
      setLoginMethod("key");
      setKeyLoginError(message ?? null);
    }} /></Suspense>;
  }

  if (bootstrapRequired && !passwordRequired) {
    return <BootstrapSetupScreen />;
  }

  if ((passwordRequired || authMode === "trusted_header") && !authenticated) {
    if (totpRequiredOnLogin) {
      return <TotpDialog open />;
    }
    return (
      <div className="relative flex min-h-screen items-center justify-center p-4">
        {/* Background decoration */}
        <div className="pointer-events-none absolute inset-0 overflow-hidden">
          <div className="absolute -top-1/4 -right-1/4 h-[600px] w-[600px] rounded-full bg-primary/5 blur-3xl" />
          <div className="absolute -bottom-1/4 -left-1/4 h-[500px] w-[500px] rounded-full bg-primary/3 blur-3xl" />
          <div className="absolute bottom-0 left-1/2 h-[400px] w-[400px] -translate-x-1/2 rounded-full bg-primary/4 blur-3xl" />
        </div>

        <div className="relative w-full max-w-sm animate-fade-in-up">
          <div className="mb-8 flex flex-col items-center gap-3 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 shadow-sm ring-2 ring-primary/10 ring-offset-2 ring-offset-background">
              <CodexLogo size={28} className="text-primary" />
            </div>
            <div>
              <h1 className="text-xl font-semibold tracking-tight">{t("auth.appTitle")}</h1>
              <p className="mt-0.5 text-sm text-muted-foreground">{t("auth.appSubtitle")}</p>
            </div>
          </div>
          <fieldset className="mb-4 flex flex-wrap justify-center gap-4 text-sm" disabled={loading || keyLoginBusy}>
            <legend className="sr-only">{t("auth.login.method")}</legend>
            <label className="flex cursor-pointer items-center gap-2">
              <input type="radio" name="login-method" value="admin" checked={loginMethod === "admin"}
                onChange={() => { setLoginMethod("admin"); setKeyLoginError(null); }} />
              {t("auth.login.administrator")}
            </label>
            <label className="flex cursor-pointer items-center gap-2">
              <input type="radio" name="login-method" value="key" checked={loginMethod === "key"}
                onChange={() => { setLoginMethod("key"); setKeyLoginError(null); }} />
              {t("keyReports.title")}
            </label>
          </fieldset>
          {loginMethod === "key" ? <KeyLoginForm initialError={keyLoginError} busy={keyLoginBusy}
            onBusyChange={setKeyLoginBusy} onLogin={() => setKeySession(true)} />
            : authMode === "trusted_header" ? (
              <div className="rounded-2xl border bg-card p-6 shadow-sm">
                <h2 className="text-lg font-semibold tracking-tight">{t("auth.trustedHeader.title")}</h2>
                <p className="mt-2 text-sm text-muted-foreground">{t("auth.trustedHeader.body")}</p>
              </div>
            ) : <LoginForm />}
        </div>
      </div>
    );
  }

  return <>{children}</>;
}
