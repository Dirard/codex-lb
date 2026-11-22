import { useState } from "react";
import { Orbit } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { buildSettingsUpdateRequest } from "@/features/settings/payload";
import { AntigravityClientVersionSchema } from "@/features/settings/schemas";
import type { DashboardSettings, SettingsUpdateRequest } from "@/features/settings/schemas";

export type AntigravityClientSettingsProps = {
  settings: DashboardSettings;
  busy: boolean;
  onSave: (payload: SettingsUpdateRequest) => Promise<void>;
};

const MAX_ANTIGRAVITY_CLIENT_VERSION_LENGTH = 20;

export function AntigravityClientSettings({ settings, busy, onSave }: AntigravityClientSettingsProps) {
  const { t } = useTranslation();
  const [version, setVersion] = useState(settings.antigravityClientVersion);

  const trimmed = version.trim();
  const valid = AntigravityClientVersionSchema.safeParse(version).success;
  const changed = valid && trimmed !== settings.antigravityClientVersion;
  const save = () =>
    void onSave(buildSettingsUpdateRequest(settings, { antigravityClientVersion: trimmed }));

  return (
    <section className="rounded-xl border bg-card p-5">
      <div className="space-y-3">
        <div className="flex items-center gap-2.5">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
            <Orbit className="h-4 w-4 text-primary" aria-hidden="true" />
          </div>
          <div>
            <h3 className="text-sm font-semibold">{t("settings.antigravityClient.title")}</h3>
            <p className="text-xs text-muted-foreground">
              {t("settings.antigravityClient.description")}
            </p>
          </div>
        </div>

        <div className="flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <p className="text-sm font-medium">{t("settings.antigravityClient.version.label")}</p>
            <p className="text-xs text-muted-foreground">
              {t("settings.antigravityClient.version.effect")}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Input
              type="text"
              value={version}
              disabled={busy}
              maxLength={MAX_ANTIGRAVITY_CLIENT_VERSION_LENGTH}
              spellCheck={false}
              autoComplete="off"
              aria-label={t("settings.antigravityClient.version.ariaLabel")}
              aria-invalid={!valid}
              onChange={(event) => setVersion(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && changed) {
                  save();
                }
              }}
              className="h-8 w-40 text-xs"
            />
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="h-8 text-xs"
              disabled={busy || !changed}
              onClick={save}
            >
              {t("settings.antigravityClient.version.save")}
            </Button>
          </div>
        </div>

        {!valid ? (
          <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs font-medium text-destructive">
            {t("settings.antigravityClient.version.invalid")}
          </div>
        ) : null}
      </div>
    </section>
  );
}
