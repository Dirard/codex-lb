import { describe, expect, it } from "vitest";

import {
  AntigravitySignInCompleteRequestSchema,
  AntigravitySignInStartSchema,
  ModelSourceCreateRequestSchema,
  ModelSourceSchema,
  ModelSourcesResponseSchema,
  ModelSourceUpdateRequestSchema,
} from "@/features/model-sources/schemas";

const ISO = "2026-01-01T00:00:00+00:00";

const BASE_SOURCE = {
  id: "src_vllm",
      name: "vLLM",
      kind: "openai_compatible",
      baseUrl: "http://localhost:8000/v1",
      isEnabled: true,
      healthStatus: "unknown",
      supportsChatCompletions: true,
      supportsResponses: false,
      supportsAudioTranscriptions: true,
      supportsEmbeddings: true,
      timeoutSeconds: null,
      maxConcurrency: null,
      createdAt: ISO,
      updatedAt: ISO,
      models: [
        {
          id: 1,
          sourceId: "src_vllm",
          model: "local-coder",
          displayName: "Local Coder",
          contextWindow: 8192,
          maxOutputTokens: 1024,
          supportsStreaming: true,
          supportsTools: true,
          supportsVision: false,
          inputPer1M: null,
          cachedInputPer1M: null,
          outputPer1M: null,
          rawMetadataJson: null,
          isEnabled: true,
          createdAt: ISO,
          updatedAt: ISO,
        },
  ],
};

describe("ModelSourceSchema", () => {
  it("parses model source payload", () => {
    const parsed = ModelSourceSchema.parse(BASE_SOURCE);

    expect(parsed.id).toBe("src_vllm");
    expect(parsed.supportsAudioTranscriptions).toBe(true);
    expect(parsed.supportsEmbeddings).toBe(true);
    expect(parsed.models[0].model).toBe("local-coder");
  });

  it("defaults supportsEmbeddings to false when the field is absent", () => {
    const withoutEmbeddings: Record<string, unknown> = { ...BASE_SOURCE };
    delete withoutEmbeddings.supportsEmbeddings;

    const parsed = ModelSourceSchema.parse(withoutEmbeddings);

    expect(parsed.supportsEmbeddings).toBe(false);
  });
});

describe("ModelSourcesResponseSchema", () => {
  it("defaults sources to an empty list", () => {
    const parsed = ModelSourcesResponseSchema.parse({});

    expect(parsed.sources).toEqual([]);
  });
});

describe("ModelSourceCreateRequestSchema", () => {
  it("accepts OpenAI-compatible source creation payload", () => {
    const parsed = ModelSourceCreateRequestSchema.parse({
      name: "DeepSeek",
      baseUrl: "https://api.deepseek.com/v1",
      apiKey: "secret",
      supportsChatCompletions: true,
      supportsResponses: true,
      supportsAudioTranscriptions: true,
      supportsEmbeddings: true,
      models: [{ model: "deepseek-v4-flash" }],
    });

    expect(parsed.supportsAudioTranscriptions).toBe(true);
    expect(parsed.supportsEmbeddings).toBe(true);
    expect(parsed.models[0].model).toBe("deepseek-v4-flash");
  });
});

describe("ModelSourceUpdateRequestSchema", () => {
  it("accepts disabling a source", () => {
    const parsed = ModelSourceUpdateRequestSchema.parse({
      isEnabled: false,
    });

    expect(parsed.isEnabled).toBe(false);
  });
});

describe("Grok subscription sources", () => {
  it("keep the account link in both directions", () => {
    const source = ModelSourceSchema.parse({ ...BASE_SOURCE, kind: "grok_subscription", providerConfig: { grokAccountId: "vacc_alpha" } });
    expect(source.providerConfig?.grokAccountId).toBe("vacc_alpha");
    const request = ModelSourceCreateRequestSchema.parse({
      kind: "grok_subscription", name: "Grok", baseUrl: "https://api.x.ai/v1", providerConfig: { grokAccountId: "vacc_alpha" },
    });
    expect(request).toMatchObject({ kind: "grok_subscription", providerConfig: { grokAccountId: "vacc_alpha" }, models: [] });
  });
});

describe("Antigravity sources", () => {
  it("carry the signed-in account and its status, never a token", () => {
    const source = ModelSourceSchema.parse({
      ...BASE_SOURCE,
      kind: "antigravity",
      accountStatus: "deactivated",
      deactivationReason: "tos_violation",
      providerConfig: {
        antigravity: { email: "agent@example.test", project: "aicode-consumers", tier: "g1-pro-tier", actionUrl: "https://forms.gle/appeal" },
      },
    });
    expect(source.providerConfig?.antigravity).toEqual({
      email: "agent@example.test", project: "aicode-consumers", tier: "g1-pro-tier", actionUrl: "https://forms.gle/appeal",
    });
    expect(source.accountStatus).toBe("deactivated");
    expect(source.deactivationReason).toBe("tos_violation");
  });

  it("validate sign-in answers and requests", () => {
    expect(AntigravitySignInStartSchema.parse({
      flowId: "agsign_x", authorizationUrl: "https://accounts.google.com/o/oauth2/v2/auth", expiresAt: "2026-11-22T12:10:00Z",
    }).flowId).toBe("agsign_x");
    expect(AntigravitySignInCompleteRequestSchema.parse({ code: " 4/code " }).code).toBe("4/code");
    expect(() => AntigravitySignInCompleteRequestSchema.parse({ code: "  " })).toThrow();
    expect(() => ModelSourceCreateRequestSchema.parse({ kind: "antigravity", name: "x", baseUrl: "https://x" })).toThrow();
  });
});
