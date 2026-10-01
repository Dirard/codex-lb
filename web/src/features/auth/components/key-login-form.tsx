import { useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { createKeyReportSession } from "@/features/key-reports/api";
import { ApiError } from "@/lib/api-client";

// Authenticate the selected key without creating an administrator session.
export function KeyLoginForm({ initialError, onLogin, busy, onBusyChange }: {
  initialError: string | null;
  onLogin: () => void;
  busy: boolean;
  onBusyChange: (busy: boolean) => void;
}) {
  const { t } = useTranslation();
  const [key, setKey] = useState("");
  const [error, setError] = useState(initialError);
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => pending.current?.abort(), []);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const apiKey = key.trim();
    if (!apiKey || busy) return;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    onBusyChange(true);
    setError(null);
    try {
      await createKeyReportSession(apiKey, controller.signal);
      if (!controller.signal.aborted) {
        setKey("");
        onLogin();
      }
    } catch (failure) {
      if (!controller.signal.aborted) setError(t(failure instanceof ApiError && failure.status === 401 ? "keyReports.invalidKey" : "keyReports.loadFailed"));
    } finally {
      if (!controller.signal.aborted) onBusyChange(false);
    }
  };

  return (
    <form onSubmit={submit} aria-busy={busy} className="space-y-5 rounded-2xl border bg-card p-6 shadow-[var(--shadow-md)]">
      <div className="space-y-1.5">
        <h2 className="text-base font-semibold tracking-tight">{t("auth.login.heading")}</h2>
        <p className="text-sm text-muted-foreground">{t("keyReports.description")}</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="report-api-key">{t("keyReports.apiKey")}</Label>
        <Input id="report-api-key" type="password" autoComplete="off" spellCheck={false} maxLength={4096}
          value={key} disabled={busy} onChange={(event) => setKey(event.target.value)} />
        <p className="text-muted-foreground text-xs">{t("keyReports.keyHelp")}</p>
      </div>
      {error ? <div role="alert"><AlertMessage variant="error">{error}</AlertMessage></div> : null}
      <Button className="w-full" type="submit" disabled={busy || !key.trim()}>{busy ? t("common.loading") : t("keyReports.signIn")}</Button>
    </form>
  );
}
