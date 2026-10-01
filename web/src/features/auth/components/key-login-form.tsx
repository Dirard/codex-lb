import { useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getKeyReport, type KeyReportSession } from "@/features/key-reports/api";
import { daysAgoLocalISO, getBrowserReportsTimeZone, localDateISO } from "@/features/reports/date";
import { ApiError } from "@/lib/api-client";

// Authenticate the selected key without creating an administrator session.
export function KeyLoginForm({ initialError, onLogin }: {
  initialError: string | null;
  onLogin: (session: KeyReportSession) => void;
}) {
  const { t } = useTranslation();
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
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
    setBusy(true);
    setError(null);
    const filters = { startDate: daysAgoLocalISO(6), endDate: localDateISO(), timezone: getBrowserReportsTimeZone(), model: "" };
    try {
      const report = await getKeyReport(apiKey, filters, controller.signal);
      if (!controller.signal.aborted) onLogin({ apiKey, report, filters });
    } catch (failure) {
      if (!controller.signal.aborted) setError(t(failure instanceof ApiError && failure.status === 401 ? "keyReports.invalidKey" : "keyReports.loadFailed"));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
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
