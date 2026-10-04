import { z } from "zod";

export const RuntimeVersionSchema = z.object({
  currentVersion: z.string(),
  latestVersion: z.string().nullable().optional(),
  updateAvailable: z.boolean(),
  checkedAt: z.string().nullable(),
  source: z.string().nullable().optional(),
  releaseUrl: z.url(),
});

export type RuntimeVersion = z.infer<typeof RuntimeVersionSchema>;

export const RuntimeUpdateStatusSchema = z.object({
  currentVersion: z.string(),
  latestVersion: z.string().optional(),
  updateAvailable: z.boolean(),
  checkedAt: z.string().nullable(),
  source: z.string(),
  releaseUrl: z.url(),
  supported: z.boolean(),
  unavailableReason: z.string().optional(),
  previousVersion: z.string().optional(),
  canRollback: z.boolean(),
  phase: z.string(),
  targetVersion: z.string().optional(),
  lastError: z.string().optional(),
});

export type RuntimeUpdateStatus = z.infer<typeof RuntimeUpdateStatusSchema>;
