import { z } from "zod";

export const VideoAccountSchema = z.object({
  id: z.string(),
  email: z.string(),
  label: z.string(),
  status: z.string(),
  cooldownUntil: z.string().nullable(),
  lastRefusalCode: z.string(),
  lastUsedAt: z.string().nullable(),
  createdAt: z.string(),
  unfinishedJobs: z.number(),
  doneJobs: z.number(),
  costUsd: z.number(),
});

export const VideoAccountsResponseSchema = z.object({
  accounts: z.array(VideoAccountSchema),
});

export const VideoImportedAccountSchema = z.object({
  id: z.string(),
  email: z.string(),
});

export const VideoKeySchema = z.object({
  id: z.string(),
  name: z.string(),
  keyPrefix: z.string(),
  isActive: z.boolean(),
  allowed: z.boolean(),
});

export const VideoKeysResponseSchema = z.object({
  keys: z.array(VideoKeySchema),
});

export const VideoJobSchema = z.object({
  id: z.string(),
  keyName: z.string(),
  accountLabel: z.string(),
  model: z.string(),
  mode: z.string(),
  requestedSeconds: z.number(),
  status: z.string(),
  durationSeconds: z.number().nullable(),
  costUsd: z.number().nullable(),
  errorCode: z.string(),
  createdAt: z.string(),
  finishedAt: z.string().nullable(),
});

export const VideoJobsResponseSchema = z.object({
  jobs: z.array(VideoJobSchema),
  nextCursor: z.string().nullable(),
});

const VideoTotalsSchema = z.object({
  jobs: z.number(),
  done: z.number(),
  seconds: z.number(),
  costUsd: z.number(),
});

export const VideoSummarySchema = z.object({
  today: VideoTotalsSchema,
  week: VideoTotalsSchema,
  month: VideoTotalsSchema,
});

export type VideoAccount = z.infer<typeof VideoAccountSchema>;
export type VideoKey = z.infer<typeof VideoKeySchema>;
export type VideoJob = z.infer<typeof VideoJobSchema>;
export type VideoSummary = z.infer<typeof VideoSummarySchema>;
