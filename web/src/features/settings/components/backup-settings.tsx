import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { HardDriveDownload } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { AlertMessage } from "@/components/alert-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError } from "@/lib/api-client";
import {
  BACKUP_MAX_INTERVAL_HOURS,
  BACKUP_MAX_KEEP,
  BACKUP_MIN_KEEP,
  BACKUPS_QUERY_KEY,
  backupDownloadUrl,
  createBackup,
  formatBytes,
  getBackupStatus,
  saveBackupSchedule,
  type BackupSchedule,
} from "@/features/settings/backups";
import { formatLocalDateTimeSeconds } from "@/utils/formatters";
import { getErrorMessageOrNull } from "@/utils/errors";

const INTEGER_PATTERN = /^\d+$/;

function parseSchedule(interval: string, keep: string): BackupSchedule | null {
  if (!INTEGER_PATTERN.test(interval.trim()) || !INTEGER_PATTERN.test(keep.trim())) return null;
  const schedule = { intervalHours: Number(interval), keep: Number(keep) };
  if (schedule.intervalHours > BACKUP_MAX_INTERVAL_HOURS) return null;
  if (schedule.keep < BACKUP_MIN_KEEP || schedule.keep > BACKUP_MAX_KEEP) return null;
  return schedule;
}

export function BackupSettings({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const statusQuery = useQuery({
    queryKey: BACKUPS_QUERY_KEY,
    queryFn: getBackupStatus,
    retry: false,
    refetchInterval: (query) => {
      if (query.state.error instanceof ApiError && query.state.error.status === 401) return false;
      return query.state.data?.running ? 2_000 : 60_000;
    },
    refetchIntervalInBackground: false,
  });
  const createMutation = useMutation({
    mutationFn: createBackup,
    onSettled: () => queryClient.invalidateQueries({ queryKey: BACKUPS_QUERY_KEY }),
  });
  const scheduleMutation = useMutation({
    mutationFn: saveBackupSchedule,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: BACKUPS_QUERY_KEY }),
  });
  const status = statusQuery.data;
  const loadError = statusQuery.isError ? getErrorMessageOrNull(statusQuery.error, t("settings.backups.loadFailed")) : null;
  const actionError = getErrorMessageOrNull(createMutation.error) || getErrorMessageOrNull(scheduleMutation.error);
  const busy = createMutation.isPending || scheduleMutation.isPending || !!status?.running;

  return (
    <section className="rounded-xl border bg-card p-5" aria-labelledby="backup-settings-title">
      <div className="flex items-start gap-2.5">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
          <HardDriveDownload className="h-4 w-4 text-primary" aria-hidden="true" />
        </div>
        <div>
          <h3 id="backup-settings-title" className="text-sm font-semibold">{t("settings.backups.title")}</h3>
          <p className="text-xs text-muted-foreground">{t("settings.backups.description")}</p>
        </div>
      </div>

      <div className="mt-4">
        <AlertMessage variant="warning">{t("settings.backups.keyWarning")}</AlertMessage>
      </div>

      {status ? (
        <div className="mt-4 space-y-4 text-sm">
          <p className="text-xs text-muted-foreground">
            {t("settings.backups.storage", {
              database: formatBytes(status.storage.databaseBytes),
              wal: formatBytes(status.storage.walBytes),
              backups: formatBytes(status.storage.backupBytes),
              free: formatBytes(status.storage.freeBytes),
            })}
          </p>
          {status.lastAttempt ? (
            <p role="status" aria-live="polite" className={status.lastAttempt.succeeded ? "" : "text-destructive"}>
              {t("settings.backups.lastAttempt", { time: formatLocalDateTimeSeconds(status.lastAttempt.at) })}{" "}
              {status.lastAttempt.succeeded
                ? t("settings.backups.lastSucceeded")
                : t("settings.backups.lastFailed", { code: status.lastAttempt.errorCode ?? "backup_failed" })}
            </p>
          ) : null}
          <BackupScheduleForm
            key={`${status.schedule.intervalHours}:${status.schedule.keep}`}
            schedule={status.schedule}
            disabled={!canWrite || scheduleMutation.isPending}
            onSave={(schedule) => scheduleMutation.mutate(schedule)}
          />
          {status.backups.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("settings.backups.empty")}</p>
          ) : (
            <ul className="divide-y rounded-lg border" aria-label={t("settings.backups.listLabel")}>
              {status.backups.map((backup) => (
                <li key={backup.name} className="flex flex-col gap-1 p-3 sm:flex-row sm:items-center sm:justify-between">
                  <div>
                    <p className="font-mono text-xs">{backup.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {formatLocalDateTimeSeconds(backup.createdAt)} · {formatBytes(backup.sizeBytes)}
                    </p>
                  </div>
                  <a
                    href={backupDownloadUrl(backup.name)}
                    download={backup.name}
                    className="text-xs text-primary underline-offset-4 hover:underline"
                    aria-label={t("settings.backups.downloadAria", { name: backup.name })}
                  >
                    {t("settings.backups.download")}
                  </a>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : statusQuery.isPending ? (
        <p className="mt-4 text-sm text-muted-foreground">{t("settings.backups.loading")}</p>
      ) : null}

      {loadError ? <div className="mt-3" role="alert"><AlertMessage variant="error">{loadError}</AlertMessage></div> : null}
      {actionError ? <div className="mt-3" role="alert"><AlertMessage variant="error">{actionError}</AlertMessage></div> : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <Button type="button" size="sm" disabled={!canWrite || !status || busy} onClick={() => createMutation.mutate()}>
          {busy && !scheduleMutation.isPending ? t("settings.backups.creating") : t("settings.backups.createNow")}
        </Button>
        {loadError ? (
          <Button type="button" size="sm" variant="outline" onClick={() => void statusQuery.refetch()}>
            {t("common.actions.retry")}
          </Button>
        ) : null}
      </div>
    </section>
  );
}

function BackupScheduleForm({
  schedule,
  disabled,
  onSave,
}: {
  schedule: BackupSchedule;
  disabled: boolean;
  onSave: (schedule: BackupSchedule) => void;
}) {
  const { t } = useTranslation();
  const [interval, setIntervalHours] = useState(String(schedule.intervalHours));
  const [keep, setKeep] = useState(String(schedule.keep));
  const parsed = parseSchedule(interval, keep);
  const changed = parsed !== null && (parsed.intervalHours !== schedule.intervalHours || parsed.keep !== schedule.keep);

  return (
    <div className="space-y-2">
      <div className="divide-y rounded-lg border">
        <label className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between">
          <span>
            <span className="block text-sm font-medium">{t("settings.backups.interval")}</span>
            <span className="block text-xs text-muted-foreground">
              {schedule.intervalHours === 0 ? t("settings.backups.disabled") : t("settings.backups.intervalHint")}
            </span>
          </span>
          <span className="flex items-center gap-2">
            <Input
              type="number" min={0} max={BACKUP_MAX_INTERVAL_HOURS} step={1} inputMode="numeric"
              value={interval} disabled={disabled} onChange={(event) => setIntervalHours(event.target.value)}
              className="h-8 w-24 text-xs" aria-label={t("settings.backups.intervalAria")}
            />
            <span className="text-xs text-muted-foreground">{t("settings.backups.hoursSuffix")}</span>
          </span>
        </label>
        <label className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between">
          <span>
            <span className="block text-sm font-medium">{t("settings.backups.keep")}</span>
            <span className="block text-xs text-muted-foreground">{t("settings.backups.keepHint")}</span>
          </span>
          <Input
            type="number" min={BACKUP_MIN_KEEP} max={BACKUP_MAX_KEEP} step={1} inputMode="numeric"
            value={keep} disabled={disabled} onChange={(event) => setKeep(event.target.value)}
            className="h-8 w-24 text-xs" aria-label={t("settings.backups.keepAria")}
          />
        </label>
      </div>
      {parsed === null ? (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs font-medium text-destructive">
          {t("settings.backups.invalidSchedule")}
        </div>
      ) : null}
      <div className="flex justify-end">
        <Button
          type="button" size="sm" variant="outline" className="h-8 text-xs"
          disabled={disabled || !changed} onClick={() => parsed && onSave(parsed)}
        >
          {t("settings.backups.save")}
        </Button>
      </div>
    </div>
  );
}
