import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { useAuthStore } from "@/features/auth/hooks/use-auth";
import {
  applyRuntimeUpdate,
  checkRuntimeUpdates,
  getRuntimeUpdateStatus,
  rollbackRuntimeUpdate,
} from "@/features/runtime/api";
import { server } from "@/test/mocks/server";

describe("runtime update API authentication", () => {
  it("routes real 401 responses from status and every action through normal logout handling", async () => {
    const before = useAuthStore.getState();
    const refreshSession = vi.fn().mockRejectedValue(new Error("session expired"));
    const unauthorized = () => HttpResponse.json(
      { error: { code: "unauthorized", message: "Session expired" } },
      { status: 401 },
    );
    server.use(
      http.get("/api/runtime/updates", unauthorized),
      http.post("/api/runtime/updates/check", unauthorized),
      http.post("/api/runtime/updates/apply", unauthorized),
      http.post("/api/runtime/updates/rollback", unauthorized),
    );

    try {
      useAuthStore.setState({ refreshSession });
      for (const [index, action] of [
        getRuntimeUpdateStatus,
        checkRuntimeUpdates,
        () => applyRuntimeUpdate("go-v1.1.0"),
        () => rollbackRuntimeUpdate("go-v1.0.0"),
      ].entries()) {
        useAuthStore.setState({ authenticated: true, initialized: true, canWrite: true });
        await expect(action()).rejects.toMatchObject({ status: 401 });
        expect(useAuthStore.getState().authenticated).toBe(false);
        expect(refreshSession).toHaveBeenCalledTimes(index + 1);
      }
    } finally {
      useAuthStore.setState(before, true);
    }
  });
});
