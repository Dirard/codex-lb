import { z } from "zod";

export const ModelSourceModelSchema = z.object({
  id: z.number(),
  sourceId: z.string(),
  model: z.string(),
  aliases: z.array(z.string()).optional(),
  upstreamModel: z.string().optional(),
  displayName: z.string().nullable().default(null),
  contextWindow: z.number().int().positive().nullable().default(null),
  maxOutputTokens: z.number().int().positive().nullable().default(null),
  supportsStreaming: z.boolean().default(true),
  supportsTools: z.boolean().default(false),
  supportsVision: z.boolean().default(false),
  inputPer1M: z.number().nullable().default(null),
  cachedInputPer1M: z.number().nullable().default(null),
  outputPer1M: z.number().nullable().default(null),
  audioPerMinute: z.number().nullable().default(null),
  rawMetadataJson: z.string().nullable().default(null),
  isEnabled: z.boolean().default(true),
  createdAt: z.iso.datetime({ offset: true }),
  updatedAt: z.iso.datetime({ offset: true }),
});

/** The signed-in Google account of an Antigravity source; never a token. */
export const AntigravityAccountInfoSchema = z.object({
  email: z.string(),
  project: z.string(),
  tier: z.string().optional(),
  /** Appeal link of a suspended account or verification link Google asks for. */
  actionUrl: z.string().optional(),
});

export const ModelSourceSchema = z.object({
  id: z.string(),
  name: z.string(),
  kind: z.string(),
  baseUrl: z.string(),
  isEnabled: z.boolean(),
  healthStatus: z.string(),
  supportsChatCompletions: z.boolean(),
  supportsResponses: z.boolean(),
  supportsAudioTranscriptions: z.boolean().default(false),
  supportsEmbeddings: z.boolean().default(false),
  timeoutSeconds: z.number().int().positive().nullable().default(null),
  maxConcurrency: z.number().int().positive().nullable().default(null),
  providerConfig: z
    .object({
      grokAccountId: z.string().optional(),
      antigravity: AntigravityAccountInfoSchema.optional(),
    })
    .optional(),
  /** Conversations started on the source stay on it when its limit runs out. */
  stickyOnly: z.boolean().default(false),
  /** Status of the source's account row, e.g. reauth_required or deactivated. */
  accountStatus: z.string().optional(),
  deactivationReason: z.string().optional(),
  createdAt: z.iso.datetime({ offset: true }),
  updatedAt: z.iso.datetime({ offset: true }),
  models: z.array(ModelSourceModelSchema).default([]),
});

export const ModelSourcesResponseSchema = z.object({
  sources: z.array(ModelSourceSchema).default([]),
});

export const ModelSourceModelInputSchema = z.object({
  model: z.string().min(1).max(255),
  aliases: z.array(z.string()).optional(),
  upstreamModel: z.string().max(255).optional(),
  displayName: z.string().max(255).nullable().optional(),
  contextWindow: z.number().int().positive().nullable().optional(),
  maxOutputTokens: z.number().int().positive().nullable().optional(),
  supportsStreaming: z.boolean().optional(),
  supportsTools: z.boolean().optional(),
  supportsVision: z.boolean().optional(),
  inputPer1M: z.number().nonnegative().nullable().optional(),
  cachedInputPer1M: z.number().nonnegative().nullable().optional(),
  outputPer1M: z.number().nonnegative().nullable().optional(),
  audioPerMinute: z.number().nonnegative().nullable().optional(),
  rawMetadataJson: z.string().nullable().optional(),
  isEnabled: z.boolean().optional(),
});

export const ModelSourceCreateRequestSchema = z.object({
  kind: z.enum(["openai_compatible", "zai", "grok_subscription"]).optional(),
  name: z.string().min(1).max(128),
  baseUrl: z.string().min(1).max(2048),
  apiKey: z.string().min(1).nullable().optional(),
  supportsChatCompletions: z.boolean().optional(),
  supportsResponses: z.boolean().optional(),
  supportsAudioTranscriptions: z.boolean().optional(),
  supportsEmbeddings: z.boolean().optional(),
  timeoutSeconds: z.number().int().positive().nullable().optional(),
  maxConcurrency: z.number().int().positive().nullable().optional(),
  // A Grok subscription source names the signed-in Grok account it borrows
  // its token from; the server then fills its models from xAI.
  providerConfig: z.object({ grokAccountId: z.string().min(1) }).optional(),
  models: z.array(ModelSourceModelInputSchema).default([]),
});

export const ModelSourceUpdateRequestSchema = z.object({
  name: z.string().min(1).max(128).optional(),
  baseUrl: z.string().min(1).max(2048).optional(),
  apiKey: z.string().min(1).nullable().optional(),
  isEnabled: z.boolean().optional(),
  supportsChatCompletions: z.boolean().optional(),
  supportsResponses: z.boolean().optional(),
  supportsAudioTranscriptions: z.boolean().optional(),
  supportsEmbeddings: z.boolean().optional(),
  timeoutSeconds: z.number().int().positive().nullable().optional(),
  maxConcurrency: z.number().int().positive().nullable().optional(),
  models: z.array(ModelSourceModelInputSchema).optional(),
});

export const AntigravitySignInStartSchema = z.object({
  flowId: z.string(),
  authorizationUrl: z.string(),
  expiresAt: z.iso.datetime({ offset: true }),
});

export const AntigravitySignInCompleteRequestSchema = z.object({
  code: z.string().trim().min(1).max(4096),
});

export type ModelSource = z.infer<typeof ModelSourceSchema>;
export type AntigravityAccountInfo = z.infer<typeof AntigravityAccountInfoSchema>;
export type AntigravitySignInStart = z.infer<typeof AntigravitySignInStartSchema>;
export type ModelSourceModel = z.infer<typeof ModelSourceModelSchema>;
export type ModelSourceModelInput = z.infer<typeof ModelSourceModelInputSchema>;
export type ModelSourceCreateRequest = z.infer<typeof ModelSourceCreateRequestSchema>;
export type ModelSourceUpdateRequest = z.infer<typeof ModelSourceUpdateRequestSchema>;
