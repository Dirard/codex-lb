/// <reference types="node" />

import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { createServer } from "node:http";
import path from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { server as mocks } from "@/test/mocks/server";
import { AuthSessionSchema } from "@/features/auth/schemas";
import { AccountsResponseSchema, RuntimeConnectAddressResponseSchema } from "@/features/accounts/schemas";
import { AccountGroupListSchema, AccountGroupSchema } from "@/features/account-groups/schemas";
import { ApiKeyCreateResponseSchema, ApiKeyListSchema, ApiKeySchema, ModelsResponseSchema } from "@/features/api-keys/schemas";
import { DashboardSettingsSchema } from "@/features/settings/schemas";
import { buildSettingsUpdateRequest } from "@/features/settings/payload";
import { ConversationsResponseSchema, DashboardOverviewSchema, DashboardProjectionsSchema, RequestLogsResponseSchema } from "@/features/dashboard/schemas";
import { ReportsResponseSchema } from "@/features/reports/schemas";
import { ModelSourceSchema, ModelSourcesResponseSchema } from "@/features/model-sources/schemas";
import { ModelPriceDeleteSchema, ModelPriceSaveSchema, ModelPricesSchema } from "@/features/model-prices/model-price";
import { ConversationArchiveFileSchema, ConversationArchiveRecordsResponseSchema } from "@/features/conversation-archive/schemas";
import { StickySessionsDeleteResponseSchema, StickySessionsListResponseSchema } from "@/features/sticky-sessions/schemas";
import { KeyReportsSchema } from "@/features/key-reports/api";

// Exercise the dashboard's real parsers against a freshly built binary rather
// than teaching mocks the same contract mistake as the server. No ChatGPT
// accounts or real provider requests are created by this offline contract test.
const binary = process.env.CODEX_LB_TEST_BINARY;

describe.skipIf(!binary)("Go runtime dashboard contracts", () => {
  let child: ChildProcess | undefined;
  let directory = "";
  let base = "";
  let cookie = "";

  beforeAll(async () => {
    mocks.close();
    directory = await mkdtemp(path.join(tmpdir(), "codex-lb-contract-"));
    await startRuntime();
    const initial = AuthSessionSchema.parse(await request("/api/dashboard-auth/session"));
    expect(initial).toMatchObject({ authenticated: false, bootstrapRequired: true, bootstrapTokenRequired: false });
    const session = AuthSessionSchema.parse(await request("/api/dashboard-auth/password/setup", "POST", { password: "synthetic-test-password" }));
    expect(session.authenticated).toBe(true);
    expect(session.bootstrapTokenRequired).toBe(false);
  });

  async function startRuntime() {
    child = spawn(binary!, ["serve", "--listen", "127.0.0.1:0", "--data-dir", directory, "--shutdown-grace", "0s"], {
      env: { PATH: process.env.PATH },
      stdio: ["ignore", "ignore", "pipe"],
    });
    base = await new Promise<string>((resolve, reject) => {
      let pending = "";
      const timeout = setTimeout(() => reject(new Error("Go server did not become ready")), 5_000);
      child!.once("error", () => { clearTimeout(timeout); reject(new Error("Cannot start Go test binary")); });
      child!.once("exit", () => { clearTimeout(timeout); reject(new Error("Go test binary exited before readiness")); });
      child!.stderr!.on("data", (chunk: Buffer) => {
        pending += chunk.toString("utf8");
        const lines = pending.split("\n");
        pending = lines.pop() ?? "";
        for (const line of lines) {
          let record: { msg?: string; address?: string };
          try { record = JSON.parse(line); } catch { continue; }
          if (record.msg === "codex-lb ready" && record.address?.startsWith("127.0.0.1:")) {
            clearTimeout(timeout);
            resolve(`http://${record.address}`);
          }
        }
      });
    });
  }

  afterAll(async () => {
    await stopRuntime();
    if (directory) await rm(directory, { recursive: true, force: true });
  });

  async function stopRuntime() {
    if (child && child.exitCode === null && child.signalCode === null) {
      await new Promise<void>((resolve) => {
        const timeout = setTimeout(() => child?.kill("SIGKILL"), 3_000);
        child!.once("exit", () => { clearTimeout(timeout); resolve(); });
        child!.kill("SIGTERM");
      });
    }
    child = undefined;
  }

  async function request(route: string, method = "GET", body?: unknown): Promise<unknown> {
    const response = await fetch(base + route, {
      method,
      headers: { ...(cookie ? { Cookie: cookie } : {}), ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(4_000),
    });
    const nextCookie = response.headers.get("set-cookie");
    if (nextCookie) cookie = nextCookie.split(";")[0];
    expect(response.status, `${method} ${route}`).toBeLessThan(300);
    if (response.status === 204) return null;
    return response.json();
  }

  it("returns parseable empty dashboard, report, model and account pages", async () => {
    AccountsResponseSchema.parse(await request("/api/accounts"));
    expect(RuntimeConnectAddressResponseSchema.parse(await request("/api/settings/runtime/connect-address")).connectAddress).toBe("<codex-lb-ip-or-dns>");
    AccountGroupListSchema.parse(await request("/api/account-groups/"));
    ApiKeyListSchema.parse(await request("/api/api-keys/"));
    ModelsResponseSchema.parse(await request("/api/models"));
    ModelSourcesResponseSchema.parse(await request("/api/model-sources"));
    ModelPricesSchema.parse(await request("/api/model-prices"));
    DashboardSettingsSchema.parse(await request("/api/settings"));
    DashboardOverviewSchema.parse(await request("/api/dashboard/overview"));
    DashboardProjectionsSchema.parse(await request("/api/dashboard/projections"));
    RequestLogsResponseSchema.parse(await request("/api/request-logs"));
    ConversationsResponseSchema.parse(await request("/api/conversations"));
    ReportsResponseSchema.parse(await request("/api/reports"));
  });

  it("saves real UI setting patches without materializing unrelated defaults", async () => {
    const before = DashboardSettingsSchema.parse(await request("/api/settings"));
    const payload = buildSettingsUpdateRequest(before, { prohibitFastMode: !before.prohibitFastMode });
    expect(Object.keys(payload).sort()).toEqual(["expectedVersion", "prohibitFastMode"]);
    const after = DashboardSettingsSchema.parse(await request("/api/settings", "PUT", payload));
    expect(after.prohibitFastMode).toBe(!before.prohibitFastMode);
    expect(after.limitWarmupModel).toBe(before.limitWarmupModel);
    expect(after.routingStrategy).toBe(before.routingStrategy);
    await request("/api/settings", "PUT", buildSettingsUpdateRequest(after, { prohibitFastMode: before.prohibitFastMode }));
  });

  it("preserves group-limit replacement and detachment through actual CRUD responses", async () => {
    const limits = [{ limitType: "total_tokens", limitWindow: "weekly", maxValue: 10000 }];
    const group = AccountGroupSchema.parse(await request("/api/account-groups/", "POST", { name: "Synthetic group", accountIds: [], limits }));
    const key = ApiKeyCreateResponseSchema.parse(await request("/api/api-keys/", "POST", { name: "Synthetic key", groupId: group.id }));
    expect(key.limits[0].maxValue).toBe(10000);
    limits[0].maxValue = 20000;
    AccountGroupSchema.parse(await request(`/api/account-groups/${group.id}`, "PUT", { name: group.name, accountIds: [], limits }));
    const keys = ApiKeyListSchema.parse(await request("/api/api-keys/"));
    expect(keys.find((item) => item.id === key.id)?.limits[0].maxValue).toBe(20000);
    const detached = ApiKeySchema.parse(await request(`/api/api-keys/${key.id}`, "PATCH", { groupId: null, usageSections: "" }));
    expect(detached.limits[0].maxValue).toBe(20000);
    expect(detached.usageSections).toBe("");
    await request(`/api/api-keys/${key.id}`, "DELETE");
    await request(`/api/account-groups/${group.id}`, "DELETE");
  });

  it("round-trips weekly pace and account capacity through real UI patches", async () => {
    const before = DashboardSettingsSchema.parse(await request("/api/settings"));
    const after = DashboardSettingsSchema.parse(await request("/api/settings", "PUT", buildSettingsUpdateRequest(before, {
      weeklyPaceWorkingDays: "0,2,4", weeklyPaceSmoothingMinutes: 120,
      proxyAccountResponseCreateLimit: 2, proxyAccountStreamLimit: 0,
    })));
    expect(after).toMatchObject({
      weeklyPaceWorkingDays: "0,2,4", weeklyPaceSmoothingMinutes: 120,
      proxyAccountResponseCreateLimit: 2, proxyAccountResponseCreateLimitOverride: 2,
      proxyAccountStreamLimit: 0, proxyAccountStreamLimitOverride: 0,
    });
    const restored = DashboardSettingsSchema.parse(await request("/api/settings", "PUT", buildSettingsUpdateRequest(after, {
      weeklyPaceWorkingDays: before.weeklyPaceWorkingDays, weeklyPaceSmoothingMinutes: before.weeklyPaceSmoothingMinutes,
      proxyAccountResponseCreateLimit: null, proxyAccountStreamLimit: null,
    })));
    expect(restored.proxyAccountResponseCreateLimit).toBe(restored.proxyAccountResponseCreateLimitEnvironmentValue);
    expect(restored.proxyAccountStreamLimit).toBe(restored.proxyAccountStreamLimitEnvironmentValue);
  });

  it("round-trips external model sources without contacting them", async () => {
    const source = ModelSourceSchema.parse(await request("/api/model-sources", "POST", {
      name: "Synthetic external source", baseUrl: "http://127.0.0.1:1/v1", models: [{ model: "synthetic-model", inputPer1M: 1, outputPer1M: 2 }],
    }));
    const page = ModelSourcesResponseSchema.parse(await request("/api/model-sources"));
    expect(page.sources[0].id).toBe(source.id);
    await request(`/api/model-sources/${source.id}`, "DELETE");
  });

  it("routes Z.AI native Responses JSON and SSE and accounts both requests", async () => {
    const observed: { path: string; body: Record<string, unknown> }[] = [];
    const upstream = createServer(async (req, res) => {
      let wire = "";
      for await (const chunk of req) wire += chunk.toString();
      const body = JSON.parse(wire) as Record<string, unknown>;
      observed.push({ path: req.url ?? "", body });
      const response = {
        id: `resp_zai_${observed.length}`, object: "response", status: "completed", model: "synthetic-zai-model",
        output: [{ type: "function_call", id: "fc_native", call_id: "call_native", name: "lookup", arguments: "{}", status: "completed" }],
        usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15, input_tokens_details: { cached_tokens: 4 } },
      };
      if (body.stream) {
        res.setHeader("Content-Type", "text/event-stream");
        res.end(`data: ${JSON.stringify({ type: "response.completed", response })}\n\ndata: [DONE]\n\n`);
      } else {
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify(response));
      }
    });
    await new Promise<void>((resolve) => upstream.listen(0, "127.0.0.1", resolve));
    try {
      const address = upstream.address();
      if (!address || typeof address === "string") throw new Error("No offline Z.AI address");
      const source = ModelSourceSchema.parse(await request("/api/model-sources", "POST", {
        kind: "zai", name: "Offline native Z.AI", baseUrl: `http://127.0.0.1:${address.port}/api/v1`,
        apiKey: "synthetic-zai-key", supportsChatCompletions: false, supportsResponses: true,
        models: [{ model: "synthetic-zai-model", supportsTools: true, supportsStreaming: true,
          rawMetadataJson: JSON.stringify({ supports_reasoning: true, supported_reasoning_levels: ["high"] }),
          inputPer1M: 1, outputPer1M: 2 }],
      }));
      expect(source).toMatchObject({ kind: "zai", supportsChatCompletions: false, supportsResponses: true });
      const key = ApiKeyCreateResponseSchema.parse(await request("/api/api-keys/", "POST", {
        name: "Offline Z.AI key", assignedSourceIds: [source.id], weeklyTokenLimit: 100000,
      }));
      const tools = [{ type: "function", name: "lookup", parameters: { type: "object", properties: {} } }];
      for (const stream of [false, true]) {
        const response = await fetch(`${base}/v1/responses`, {
          method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${key.key}` },
          body: JSON.stringify({ model: "synthetic-zai-model", input: "Synthetic prompt", tools, reasoning: { effort: "high" }, stream }),
          signal: AbortSignal.timeout(4000),
        });
        expect(response.status).toBe(200);
        const result = await response.text();
        expect(result).toContain('"call_id":"call_native"');
        if (stream) expect(result).toContain('"type":"response.completed"');
      }
      expect(observed).toHaveLength(2);
      for (const entry of observed) {
        expect(entry.path).toBe("/api/v1/responses");
        expect(entry.body).toMatchObject({ tools, reasoning: { effort: "high" } });
        for (const field of ["messages", "thinking", "reasoning_effort", "stream_options"]) {
          expect(entry.body).not.toHaveProperty(field);
        }
      }
      const summary = ApiKeyListSchema.parse(await request("/api/api-keys/")).find((item) => item.id === key.id)?.usageSummary;
      expect(summary).toMatchObject({ requestCount: 2, totalTokens: 30 });
      expect(summary?.totalCostUsd).toBeCloseTo(0.00004, 8);
      const otherKey = ApiKeyCreateResponseSchema.parse(await request("/api/api-keys/", "POST", {
        name: "Other offline key", assignedSourceIds: [source.id],
      }));
      for (const [credential, expectedRequests] of [[key.key, 2], [otherKey.key, 0]] as const) {
        const response = await fetch(`${base}/v1/usage/reports/`, {
          headers: { Authorization: `Bearer ${credential}` }, signal: AbortSignal.timeout(4000),
        });
        expect(response.status).toBe(200);
        expect(response.headers.get("cache-control")).toBe("no-store");
        const body = await response.json();
        expect(KeyReportsSchema.parse(body).summary.totalRequests).toBe(expectedRequests);
        expect(body).not.toHaveProperty("byAccount");
      }
      expect((await fetch(`${base}/v1/usage/reports?api_key_id=${otherKey.id}`, {
        headers: { Authorization: `Bearer ${key.key}` }, signal: AbortSignal.timeout(4000),
      })).status).toBe(400);
      expect((await fetch(`${base}/v1/usage/reports`, { signal: AbortSignal.timeout(4000) })).status).toBe(401);
      await request(`/api/api-keys/${otherKey.id}`, "DELETE");
      await request(`/api/api-keys/${key.id}`, "DELETE");
      await request(`/api/model-sources/${source.id}`, "DELETE");
    } finally {
      await new Promise<void>((resolve, reject) => upstream.close((err) => err ? reject(err) : resolve()));
    }
  });

  it("round-trips cache TTL and split pressure thresholds through real UI patches", async () => {
    const before = DashboardSettingsSchema.parse(await request("/api/settings"));
    const after = DashboardSettingsSchema.parse(await request("/api/settings", "PUT", buildSettingsUpdateRequest(before, {
      stickyThreadsEnabled: false, openaiCacheAffinityMaxAgeSeconds: 90,
      stickyReallocationPrimaryBudgetThresholdPct: 80, stickyReallocationSecondaryBudgetThresholdPct: 95,
    })));
    expect(after).toMatchObject({
      stickyThreadsEnabled: false, openaiCacheAffinityMaxAgeSeconds: 90,
      stickyReallocationPrimaryBudgetThresholdPct: 80, stickyReallocationSecondaryBudgetThresholdPct: 95,
      stickyReallocationBudgetThresholdPct: 80,
    });
    await request("/api/settings", "PUT", buildSettingsUpdateRequest(after, {
      stickyThreadsEnabled: before.stickyThreadsEnabled, openaiCacheAffinityMaxAgeSeconds: before.openaiCacheAffinityMaxAgeSeconds,
      stickyReallocationPrimaryBudgetThresholdPct: before.stickyReallocationPrimaryBudgetThresholdPct,
      stickyReallocationSecondaryBudgetThresholdPct: before.stickyReallocationSecondaryBudgetThresholdPct,
    }));
  });

  it("persists custom model prices across restart and restores bundled prices", async () => {
    const price = { standard: { inputMicrodollarsPerMillion: 3000000, cachedMicrodollarsPerMillion: 200000, outputMicrodollarsPerMillion: 7000000 } };
    const saved = ModelPriceSaveSchema.parse(await request("/api/model-prices/synthetic-new-model", "PUT", { price }));
    expect(saved.price).toEqual(price);
    expect(saved.reprice).toMatchObject({ applied: true, recomputedRequests: 0, costDeltaMicrodollars: 0 });
    await stopRuntime();
    cookie = "";
    await startRuntime();
    AuthSessionSchema.parse(await request("/api/dashboard-auth/password/login", "POST", { password: "synthetic-test-password" }));
    const prices = ModelPricesSchema.parse(await request("/api/model-prices")).prices;
    expect(prices.find((item) => item.model === saved.model)?.price).toEqual(price);
    const builtin = prices.find((item) => item.source === "builtin")!;
    ModelPriceSaveSchema.parse(await request(`/api/model-prices/${encodeURIComponent(builtin.model)}`, "PUT", { price }));
    expect(ModelPriceDeleteSchema.parse(await request(`/api/model-prices/${encodeURIComponent(builtin.model)}`, "DELETE")).reprice.applied).toBe(true);
    const restored = ModelPricesSchema.parse(await request("/api/model-prices")).prices.find((item) => item.model === builtin.model);
    expect(restored).toEqual(builtin);
    expect(ModelPriceDeleteSchema.parse(await request("/api/model-prices/synthetic-new-model", "DELETE")).reprice.applied).toBe(false);
  });

  it("accounts translated Responses and archives only the failing attempt", async () => {
    let fail = false;
    let calls = 0;
    const upstream = createServer((req, res) => {
      req.resume();
      calls++;
      res.setHeader("Content-Type", "application/json");
      if (fail) {
        res.writeHead(503);
        res.end(JSON.stringify({ error: { code: "synthetic_failure", message: "synthetic failure" } }));
      } else {
        res.end(JSON.stringify({ choices: [{ index: 0, message: { role: "assistant", content: "synthetic answer" } }], usage: { prompt_tokens: 12, completion_tokens: 8, total_tokens: 20 } }));
      }
    });
    await new Promise<void>((resolve) => upstream.listen(0, "127.0.0.1", resolve));
    try {
      const address = upstream.address();
      if (!address || typeof address === "string") throw new Error("No offline upstream address");
      const source = ModelSourceSchema.parse(await request("/api/model-sources", "POST", {
        name: "Offline provider", baseUrl: `http://127.0.0.1:${address.port}/v1`, apiKey: "synthetic-provider-secret", models: [{ model: "synthetic-model", inputPer1M: 1, outputPer1M: 2 }],
      }));
      const key = ApiKeyCreateResponseSchema.parse(await request("/api/api-keys/", "POST", { name: "Metered offline key", assignedSourceIds: [source.id], weeklyTokenLimit: 100000 }));
      await request("/api/settings", "PUT", { apiKeyAuthEnabled: true });
      const respond = () => fetch(`${base}/v1/responses`, { method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${key.key}` }, body: JSON.stringify({ model: "synthetic-model", input: "synthetic prompt", stream: false }), signal: AbortSignal.timeout(4000) });
      const success = await respond();
      expect(success.status).toBe(200);
      await success.json();
      const affinities = StickySessionsListResponseSchema.parse(await request("/api/sticky-sessions?sortBy=updated_at&sortDir=desc&limit=10&offset=0"));
      expect(affinities.total).toBe(1);
      expect(affinities.entries[0].kind).toBe("prompt_cache");
      expect(affinities.entries[0].key).not.toContain("synthetic prompt");
      expect(ConversationArchiveFileSchema.array().parse(await request("/api/conversation-archive/files"))).toHaveLength(0);
      fail = true;
      const failed = await respond();
      expect(failed.status).toBe(503);
      await failed.json();
      const days = ConversationArchiveFileSchema.array().parse(await request("/api/conversation-archive/files"));
      expect(days).toHaveLength(1);
      const archives = ConversationArchiveRecordsResponseSchema.parse(await request(`/api/conversation-archive/records?file=${days[0].name}`));
      expect(archives.total).toBe(1);
      expect(JSON.stringify(archives.records)).not.toContain("synthetic-provider-secret");
      const keys = ApiKeyListSchema.parse(await request("/api/api-keys/"));
      const summary = keys.find((item) => item.id === key.id)?.usageSummary;
      expect(summary?.requestCount).toBe(1);
      expect(summary?.totalTokens).toBe(20);
      expect(summary?.totalCostUsd).toBeCloseTo(0.000028, 8);
      const globalPrice = ModelPriceSaveSchema.parse(await request("/api/model-prices/synthetic-model", "PUT", {
        price: { standard: { inputMicrodollarsPerMillion: 900000000, cachedMicrodollarsPerMillion: 100000000, outputMicrodollarsPerMillion: 900000000 } },
      }));
      expect(globalPrice.reprice).toMatchObject({ recomputedRequests: 0, costDeltaMicrodollars: 0 });
      const afterReprice = ApiKeyListSchema.parse(await request("/api/api-keys/")).find((item) => item.id === key.id)?.usageSummary;
      expect(afterReprice).toEqual(summary);
      ModelPriceDeleteSchema.parse(await request("/api/model-prices/synthetic-model", "DELETE"));
      expect(calls).toBe(2);
      const usage = await fetch(`${base}/v1/usage`, { headers: { Authorization: `Bearer ${key.key}` }, signal: AbortSignal.timeout(4000) });
      expect(usage.status).toBe(200);
      expect(await usage.json()).toMatchObject({ request_count: 1, total_tokens: 20 });
      expect((await fetch(`${base}/v1/usage`, { signal: AbortSignal.timeout(4000) })).status).toBe(401);
      const codexUsage = await fetch(`${base}/api/codex/usage`, { headers: { Authorization: `Bearer ${key.key}` }, signal: AbortSignal.timeout(4000) });
      expect(codexUsage.status).toBe(200);
      expect(codexUsage.headers.get("cache-control")).toBe("no-store");
      await codexUsage.json();
      DashboardOverviewSchema.parse(await request("/api/dashboard/overview"));
      RequestLogsResponseSchema.parse(await request("/api/request-logs"));
      ReportsResponseSchema.parse(await request("/api/reports"));
      const cleared = StickySessionsDeleteResponseSchema.parse(await request("/api/sticky-sessions/delete", "POST", {
        sessions: affinities.entries.map(({ key, kind }) => ({ key, kind })),
      }));
      expect(cleared.deletedCount).toBe(1);
      expect(StickySessionsListResponseSchema.parse(await request("/api/sticky-sessions")).total).toBe(0);
      await request(`/api/api-keys/${key.id}`, "DELETE");
      await request(`/api/model-sources/${source.id}`, "DELETE");
    } finally {
      await new Promise<void>((resolve, reject) => upstream.close((err) => err ? reject(err) : resolve()));
    }
  });

  it("returns pending accounting as nullable request logs without fabricating totals", async () => {
    let calls = 0;
    const upstream = createServer((req, res) => {
      req.resume();
      calls++;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ id: "synthetic-unknown", object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content: "answer without usage" }, finish_reason: "stop" }] }));
    });
    await new Promise<void>((resolve) => upstream.listen(0, "127.0.0.1", resolve));
    try {
      const address = upstream.address();
      if (!address || typeof address === "string") throw new Error("No offline upstream address");
      const source = ModelSourceSchema.parse(await request("/api/model-sources", "POST", {
        name: "Pending accounting provider", baseUrl: `http://127.0.0.1:${address.port}/v1`, apiKey: "synthetic-only-key",
        models: [{ model: "pending-model", inputPer1M: 1, outputPer1M: 2 }],
      }));
      const key = ApiKeyCreateResponseSchema.parse(await request("/api/api-keys/", "POST", { name: "Pending accounting key", assignedSourceIds: [source.id], weeklyTokenLimit: 1 }));
      const response = await fetch(`${base}/v1/chat/completions`, {
        method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${key.key}` },
        body: JSON.stringify({ model: "pending-model", messages: [{ role: "user", content: "synthetic prompt" }] }), signal: AbortSignal.timeout(4000),
      });
      expect(response.status).toBe(502);
      await response.json();
      const page = RequestLogsResponseSchema.parse(await request(`/api/request-logs?status=reconciliation_required&apiKeyId=${key.id}`));
      expect(page.total).toBe(1);
      expect(page.requests[0]).toMatchObject({ requestKind: "unknown", status: "reconciliation_required", model: "pending-model", apiKeyId: key.id, accountId: source.id, costUsd: null, inputTokens: null, outputTokens: null, cachedInputTokens: null, tokens: null, latencyMs: null });
      const summary = ApiKeyListSchema.parse(await request("/api/api-keys/")).find((item) => item.id === key.id)?.usageSummary;
      expect(summary?.requestCount ?? 0).toBe(0);
      expect(calls).toBe(1);
      expect((await fetch(`${base}/api/request-logs`, { signal: AbortSignal.timeout(4000) })).status).toBe(401);
    } finally {
      await new Promise<void>((resolve, reject) => upstream.close((err) => err ? reject(err) : resolve()));
    }
  });
});
