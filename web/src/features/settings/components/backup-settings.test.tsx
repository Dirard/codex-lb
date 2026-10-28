import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";

import { BackupSettings } from "@/features/settings/components/backup-settings";
import type { BackupStatus } from "@/features/settings/backups";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const backupName = "codex-lb-20261028T031500Z.sqlite3";

function status(overrides: Partial<BackupStatus> = {}): BackupStatus {
  return {
    schedule: { intervalHours: 24, keep: 3 },
    backups: [{ name: backupName, sizeBytes: 5 * 1024 * 1024, createdAt: "2026-10-28T03:15:00Z" }],
    lastAttempt: { at: "2026-10-28T03:15:00Z", trigger: "scheduled", succeeded: true, errorCode: null, name: backupName },
    running: false,
    encryptionKeyIncluded: false,
    storage: { databaseBytes: 8 * 1024 * 1024, walBytes: 1024, backupBytes: 5 * 1024 * 1024, freeBytes: 10 * 1024 ** 3, totalBytes: 40 * 1024 ** 3 },
    ...overrides,
  };
}

describe("BackupSettings", () => {
  it("shows the key warning, sizes and download links for verified backups", async () => {
    server.use(http.get("/api/backups", () => HttpResponse.json(status())));
    renderWithProviders(<BackupSettings />);

    expect(await screen.findByText(backupName)).toBeInTheDocument();
    expect(screen.getByText(/do not contain encryption\.key/)).toBeInTheDocument();
    expect(screen.getByText(/Database 8\.0 MiB · WAL 1\.0 KiB · backups 5\.0 MiB · free disk 10\.0 GiB/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: `Download ${backupName}` })).toHaveAttribute("href", `/api/backups/${backupName}`);
    expect(screen.getByRole("status")).toHaveTextContent("succeeded");
  });

  it("creates a backup and saves a validated schedule", async () => {
    const user = userEvent.setup();
    let created = 0;
    const schedules: unknown[] = [];
    server.use(
      http.get("/api/backups", () => HttpResponse.json(status({ backups: [], lastAttempt: null }))),
      http.post("/api/backups", () => {
        created++;
        return HttpResponse.json({ name: backupName, sizeBytes: 10, createdAt: "2026-10-28T03:15:00Z" }, { status: 201 });
      }),
      http.put("/api/backups/schedule", async ({ request }) => {
        const body = await request.json();
        schedules.push(body);
        return HttpResponse.json(body);
      }),
    );
    renderWithProviders(<BackupSettings />);

    expect(await screen.findByText("No backups yet.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Back up now" }));
    await waitFor(() => expect(created).toBe(1));

    const interval = screen.getByRole("spinbutton", { name: "Hours between scheduled backups" });
    const keep = screen.getByRole("spinbutton", { name: "Number of backups to keep" });
    await user.clear(keep);
    await user.type(keep, "31");
    expect(screen.getByText("Use 0–168 hours and keep 1–30 backups.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save schedule" })).toBeDisabled();
    await user.clear(keep);
    await user.type(keep, "7");
    await user.clear(interval);
    await user.type(interval, "0");
    await user.click(screen.getByRole("button", { name: "Save schedule" }));
    await waitFor(() => expect(schedules).toEqual([{ intervalHours: 0, keep: 7 }]));
  });

  it("reports a failed attempt and keeps the backup list available", async () => {
    server.use(
      http.get("/api/backups", () =>
        HttpResponse.json(status({ lastAttempt: { at: "2026-10-28T03:15:00Z", trigger: "scheduled", succeeded: false, errorCode: "insufficient_space", name: null } })),
      ),
    );
    renderWithProviders(<BackupSettings />);

    const outcome = await screen.findByRole("status");
    expect(outcome).toHaveTextContent("failed (insufficient_space)");
    expect(screen.getByRole("button", { name: "Back up now" })).toBeEnabled();
    const list = screen.getByRole("list", { name: "Backups" });
    expect(within(list).getByRole("link", { name: `Download ${backupName}` })).toBeInTheDocument();
  });
});
