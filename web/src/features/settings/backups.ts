import { z } from "zod";

import { get, post, put } from "@/lib/api-client";

const BACKUPS_PATH = "/api/backups";

export const BackupFileSchema = z.object({
  name: z.string(),
  sizeBytes: z.number(),
  createdAt: z.string(),
});

export const BackupScheduleSchema = z.object({
  intervalHours: z.number().int(),
  keep: z.number().int(),
});

export const BackupStatusSchema = z.object({
  schedule: BackupScheduleSchema,
  backups: z.array(BackupFileSchema),
  lastAttempt: z
    .object({
      at: z.string(),
      trigger: z.string(),
      succeeded: z.boolean(),
      errorCode: z.string().nullable(),
      name: z.string().nullable(),
    })
    .nullable(),
  running: z.boolean(),
  encryptionKeyIncluded: z.boolean(),
  storage: z.object({
    databaseBytes: z.number(),
    walBytes: z.number(),
    backupBytes: z.number(),
    freeBytes: z.number(),
    totalBytes: z.number(),
  }),
});

export type BackupFile = z.infer<typeof BackupFileSchema>;
export type BackupSchedule = z.infer<typeof BackupScheduleSchema>;
export type BackupStatus = z.infer<typeof BackupStatusSchema>;

export const BACKUPS_QUERY_KEY = ["settings", "backups"] as const;

export const BACKUP_MAX_INTERVAL_HOURS = 168;
export const BACKUP_MIN_KEEP = 1;
export const BACKUP_MAX_KEEP = 30;

export function getBackupStatus() {
  return get(BACKUPS_PATH, BackupStatusSchema);
}

export function createBackup() {
  return post(BACKUPS_PATH, BackupFileSchema);
}

export function saveBackupSchedule(schedule: BackupSchedule) {
  return put(`${BACKUPS_PATH}/schedule`, BackupScheduleSchema, { body: schedule });
}

// Same-origin navigation: the session cookie authorizes the download and the
// server answers with an attachment, so no file content passes through JS.
export function backupDownloadUrl(name: string) {
  return `${BACKUPS_PATH}/${encodeURIComponent(name)}`;
}

export function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`;
}
