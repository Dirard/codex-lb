import { del, get, patch, post } from "@/lib/api-client";

import {
  AntigravitySignInCompleteRequestSchema,
  AntigravitySignInStartSchema,
  ModelSourceCreateRequestSchema,
  ModelSourceSchema,
  ModelSourcesResponseSchema,
  ModelSourceUpdateRequestSchema,
} from "@/features/model-sources/schemas";

const MODEL_SOURCES_PATH = "/api/model-sources";
const ANTIGRAVITY_SIGN_IN_PATH = "/api/antigravity/sign-in";

export function listModelSources() {
  return get(`${MODEL_SOURCES_PATH}/`, ModelSourcesResponseSchema);
}

export function createModelSource(payload: unknown) {
  const validated = ModelSourceCreateRequestSchema.parse(payload);
  return post(`${MODEL_SOURCES_PATH}/`, ModelSourceSchema, {
    body: validated,
  });
}

export function updateModelSource(sourceId: string, payload: unknown) {
  const validated = ModelSourceUpdateRequestSchema.parse(payload);
  return patch(`${MODEL_SOURCES_PATH}/${encodeURIComponent(sourceId)}`, ModelSourceSchema, {
    body: validated,
  });
}

export function deleteModelSource(sourceId: string) {
  return del(`${MODEL_SOURCES_PATH}/${encodeURIComponent(sourceId)}`);
}

/** Starts a Google sign-in; sourceId names the Antigravity source being re-authorized. */
export function startAntigravitySignIn(sourceId?: string) {
  return post(ANTIGRAVITY_SIGN_IN_PATH, AntigravitySignInStartSchema, {
    body: sourceId ? { sourceId } : {},
  });
}

/** Completes a sign-in with the code Google showed; answers with the created or re-authorized source. */
export function completeAntigravitySignIn(flowId: string, code: string) {
  const validated = AntigravitySignInCompleteRequestSchema.parse({ code });
  return post(`${ANTIGRAVITY_SIGN_IN_PATH}/${encodeURIComponent(flowId)}`, ModelSourceSchema, {
    body: validated,
  });
}

export function refreshAntigravityModels(sourceId: string) {
  return post(
    `${MODEL_SOURCES_PATH}/${encodeURIComponent(sourceId)}/antigravity/refresh-models`,
    ModelSourceSchema,
  );
}
