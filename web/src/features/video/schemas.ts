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
  usagePercent: z.number().nullable(),
  usagePeriodEnd: z.string().nullable(),
  usageCheckedAt: z.string().nullable(),
  usageError: z.string(),
});

export const VideoAccountsResponseSchema = z.object({
  accounts: z.array(VideoAccountSchema),
});

export const VideoLoginStartSchema = z.object({
  flowId: z.string(),
  userCode: z.string(),
  verificationUrl: z.string(),
  intervalSeconds: z.number(),
  expiresInSeconds: z.number(),
});

export const VideoLoginStatusSchema = z.object({
  status: z.enum(["pending", "success", "error"]),
  email: z.string().optional(),
  message: z.string().optional(),
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
  errorMessage: z.string(),
  upstreamHttpStatus: z.number(),
  upstreamBody: z.string(),
  upstreamAt: z.string().nullable(),
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
export type VideoLoginStart = z.infer<typeof VideoLoginStartSchema>;
export type VideoKey = z.infer<typeof VideoKeySchema>;
export type VideoJob = z.infer<typeof VideoJobSchema>;
export type VideoSummary = z.infer<typeof VideoSummarySchema>;
