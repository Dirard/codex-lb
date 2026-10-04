import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { useAuthStore } from "@/features/auth/hooks/use-auth";
import { RuntimeUpdateSettings } from "@/features/runtime/runtime-update-settings";
import type { RuntimeUpdateStatus } from "@/features/runtime/schemas";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const updateStatus: RuntimeUpdateStatus = {
  currentVersion: "go-v1.0.0",
  latestVersion: "go-v1.1.0",
  updateAvailable: true,
  checkedAt: "2026-10-04T10:00:00Z",
  source: "github",
  releaseUrl: "https://github.com/Dirard/codex-lb/releases/tag/go-v1.1.0",
  supported: true,
  previousVersion: "go-v0.9.0",
  canRollback: true,
  phase: "idle",
};

describe("RuntimeUpdateSettings", () => {
  it("checks and confirms an exact update target before posting", async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    let checks = 0;
    server.use(
      http.get("/api/runtime/updates", () => HttpResponse.json(updateStatus)),
      http.post("/api/runtime/updates/check", () => {
        checks++;
        return HttpResponse.json({ ...updateStatus, phase: "checking" }, { status: 202 });
      }),
      http.post("/api/runtime/updates/apply", async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ ...updateStatus, phase: "downloading", targetVersion: "go-v1.1.0" }, { status: 202 });
      }),
    );
    renderWithProviders(<RuntimeUpdateSettings canWrite />);

    expect(await screen.findByText("Current version: go-v1.0.0")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Release notes" })).toHaveAttribute("href", updateStatus.releaseUrl);
    await user.click(screen.getByRole("button", { name: "Check for updates" }));
    await waitFor(() => expect(checks).toBe(1));
    // A fresh status can become idle again after the check completes.
    await waitFor(() => expect(screen.getByRole("button", { name: "Update to go-v1.1.0" })).toBeEnabled());

    await user.click(screen.getByRole("button", { name: "Update to go-v1.1.0" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Update to go-v1.1.0?")).toBeInTheDocument();
    expect(within(dialog).getByText(/active requests finish/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Update" }));
    await waitFor(() => expect(posted).toEqual([{ version: "go-v1.1.0" }]));
    expect(await screen.findByText("Target version: go-v1.1.0")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Roll back to go-v0.9.0" })).toBeDisabled();
  });

  it("confirms rollback and states that application data stays in place", async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    server.use(
      http.get("/api/runtime/updates", () => HttpResponse.json(updateStatus)),
      http.post("/api/runtime/updates/rollback", async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ ...updateStatus, phase: "rolling_back", targetVersion: "go-v0.9.0" }, { status: 202 });
      }),
    );
    renderWithProviders(<RuntimeUpdateSettings canWrite />);
    await screen.findByText("Previous version: go-v0.9.0");
    await user.click(screen.getByRole("button", { name: "Roll back to go-v0.9.0" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Roll back to go-v0.9.0?")).toBeInTheDocument();
    expect(within(dialog).getByText(/Database, settings, credentials, usage and reports remain unchanged/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Roll back" }));
    await waitFor(() => expect(posted).toEqual([{ version: "go-v0.9.0" }]));
  });

  it("explains unsupported and read-only states without enabling actions", async () => {
    server.use(http.get("/api/runtime/updates", () => HttpResponse.json({
      ...updateStatus,
      supported: false,
      unavailableReason: "Executable storage is read-only",
    })));
    const first = renderWithProviders(<RuntimeUpdateSettings canWrite />);
    expect(await screen.findByText("Executable storage is read-only")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check for updates" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Update to go-v1.1.0" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Roll back to go-v0.9.0" })).toBeDisabled();
    first.unmount();

    server.use(http.get("/api/runtime/updates", () => HttpResponse.json(updateStatus)));
    renderWithProviders(<RuntimeUpdateSettings canWrite={false} />);
    await screen.findByText("Current version: go-v1.0.0");
    expect(screen.getByRole("button", { name: "Check for updates" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Update to go-v1.1.0" })).toBeDisabled();
  });

  it("shows action errors and keeps observing through network and 503 restart interruptions", async () => {
    const user = userEvent.setup();
    let statusMode: "ready" | "network" | "offline" | "done" = "ready";
    server.use(
      http.get("/api/runtime/updates", () => statusMode === "network"
        ? HttpResponse.error()
        : statusMode === "offline"
          ? HttpResponse.json({ error: { code: "unavailable", message: "Temporary restart" } }, { status: 503 })
        : HttpResponse.json(statusMode === "done" ? { ...updateStatus, phase: "succeeded", currentVersion: "go-v1.1.0" } : updateStatus)),
      http.post("/api/runtime/updates/check", () =>
        HttpResponse.json({ error: { code: "update_busy", message: "Another operation is in progress" } }, { status: 409 })),
      http.post("/api/runtime/updates/apply", () =>
        HttpResponse.json({ ...updateStatus, phase: "starting", targetVersion: "go-v1.1.0" }, { status: 202 })),
    );
    useAuthStore.setState({ authenticated: true, initialized: true });
    const { queryClient } = renderWithProviders(<RuntimeUpdateSettings canWrite />);
    await screen.findByText("Current version: go-v1.0.0");
    await user.click(screen.getByRole("button", { name: "Check for updates" }));
    expect(await screen.findByText("Another operation is in progress")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Update to go-v1.1.0" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Update" }));
    await screen.findByText("Target version: go-v1.1.0");
    statusMode = "network";
    await queryClient.invalidateQueries({ queryKey: ["runtime", "updates"] });
    expect(await screen.findByText("Reconnecting during server restart…")).toBeInTheDocument();
    expect(useAuthStore.getState().authenticated).toBe(true);
    statusMode = "offline";
    await queryClient.invalidateQueries({ queryKey: ["runtime", "updates"] });
    expect(await screen.findByText("Reconnecting during server restart…")).toBeInTheDocument();
    expect(useAuthStore.getState().authenticated).toBe(true);
    statusMode = "done";
    // The active-operation interval must reconnect without a manual refresh.
    expect(await screen.findByText("Current version: go-v1.1.0")).toBeInTheDocument();
    expect(screen.queryByText("Reconnecting during server restart…")).not.toBeInTheDocument();
  });

  it("does not present a real 401 as a restart interruption", async () => {
    const before = useAuthStore.getState();
    const user = userEvent.setup();
    let unauthorized = false;
    server.use(
      http.get("/api/runtime/updates", () => unauthorized
        ? HttpResponse.json({ error: { code: "unauthorized", message: "Session expired" } }, { status: 401 })
        : HttpResponse.json(updateStatus)),
      http.post("/api/runtime/updates/apply", () =>
        HttpResponse.json({ ...updateStatus, phase: "starting", targetVersion: "go-v1.1.0" }, { status: 202 })),
    );
    const refreshSession = vi.fn().mockRejectedValue(new Error("Session expired"));
    useAuthStore.setState({ authenticated: true, initialized: true, refreshSession });
    const rendered = renderWithProviders(<RuntimeUpdateSettings canWrite />);
    try {
      await screen.findByText("Current version: go-v1.0.0");
      await user.click(screen.getByRole("button", { name: "Update to go-v1.1.0" }));
      await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Update" }));
      await screen.findByText("Target version: go-v1.1.0");
      unauthorized = true;
      await rendered.queryClient.invalidateQueries({ queryKey: ["runtime", "updates"] });
      await waitFor(() => expect(useAuthStore.getState().authenticated).toBe(false));
      expect(refreshSession).toHaveBeenCalledOnce();
      expect(screen.queryByText("Reconnecting during server restart…")).not.toBeInTheDocument();
      expect(screen.getByText("Session expired")).toBeInTheDocument();
    } finally {
      rendered.unmount();
      useAuthStore.setState(before, true);
    }
  });
});
