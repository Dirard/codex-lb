import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { VideoPage } from "@/features/video/components/video-page";
import { server } from "@/test/mocks/server";
import { renderWithProviders } from "@/test/utils";

const account = {
  id: "vacc_1", email: "one@example.invalid", label: "Main", status: "active", cooldownUntil: null, lastRefusalCode: "",
  lastUsedAt: null, createdAt: "2026-10-31T10:00:00Z", updatedAt: "2026-10-31T10:00:00Z", unfinishedJobs: 1, doneJobs: 3, costUsd: 0.54,
};
const totals = { jobs: 3, done: 3, seconds: 6, costUsd: 0.54 };

function poolHandlers(state: { allowed: boolean }) {
  return [
    http.get("/api/video/accounts", () => HttpResponse.json({ accounts: [account] })),
    http.get("/api/video/keys", () =>
      HttpResponse.json({ keys: [{ id: "key_1", name: "Owner key", keyPrefix: "sk-clb-1", isActive: true, allowed: state.allowed }] }),
    ),
    http.put("/api/video/keys/key_1", () => {
      state.allowed = true;
      return new HttpResponse(null, { status: 204 });
    }),
    http.delete("/api/video/keys/key_1", () => {
      state.allowed = false;
      return new HttpResponse(null, { status: 204 });
    }),
    http.get("/api/video/jobs", () =>
      HttpResponse.json({
        nextCursor: null,
        jobs: [{
          id: "vid_1", keyName: "Owner key", accountLabel: "Main", model: "grok-imagine-video-1.5", mode: "first_last_frame",
          requestedSeconds: 2, status: "done", durationSeconds: 2, costUsd: 0.18, errorCode: "", createdAt: "2026-10-31T10:00:00Z", finishedAt: "2026-10-31T10:00:30Z",
        }],
      }),
    ),
    http.get("/api/video/summary", () => HttpResponse.json({ today: totals, week: totals, month: totals })),
  ];
}

describe("VideoPage", () => {
  it("shows the pool, totals and recent jobs", async () => {
    server.use(...poolHandlers({ allowed: true }));
    renderWithProviders(<VideoPage />);
    expect(await screen.findAllByText("Main")).not.toHaveLength(0);
    const jobs = await screen.findByRole("heading", { name: "Recent jobs" });
    const section = jobs.closest("section") as HTMLElement;
    expect(await within(section).findByText("First and last frame")).toBeInTheDocument();
    expect(within(section).getByText("$0.18")).toBeInTheDocument();
    expect(await screen.findByLabelText("Totals")).toBeInTheDocument();
  });

  it("adds and removes an allowed key", async () => {
    const user = userEvent.setup();
    const state = { allowed: false };
    server.use(...poolHandlers(state));
    renderWithProviders(<VideoPage />);
    const select = await screen.findByLabelText("Choose a key");
    await within(select).findByRole("option", { name: "Owner key (sk-clb-1)" });
    await user.selectOptions(select, "key_1");
    await user.click(screen.getByRole("button", { name: "Allow" }));
    const remove = await screen.findByRole("button", { name: "Remove" });
    await waitFor(() => expect(state.allowed).toBe(true));
    await user.click(remove);
    await waitFor(() => expect(state.allowed).toBe(false));
  });

  it("signs an account in from the page and pauses an account", async () => {
    const user = userEvent.setup();
    const patched = vi.fn();
    const cancelled = vi.fn();
    let polls = 0;
    server.use(
      ...poolHandlers({ allowed: true }),
      http.post("/api/video/accounts/login", () =>
        HttpResponse.json({ flowId: "vlogin_1", userCode: "ABCD-1234", verificationUrl: "https://accounts.example.invalid/device?user_code=ABCD-1234", intervalSeconds: 0, expiresInSeconds: 1800 }),
      ),
      http.get("/api/video/accounts/login/vlogin_1", () => {
        polls += 1;
        return HttpResponse.json(polls < 2 ? { status: "pending" } : { status: "success", email: "two@example.invalid" });
      }),
      http.delete("/api/video/accounts/login/vlogin_1", () => {
        cancelled();
        return new HttpResponse(null, { status: 204 });
      }),
      http.patch("/api/video/accounts/vacc_1", async ({ request }) => {
        patched(await request.json());
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderWithProviders(<VideoPage />);
    await screen.findAllByText("Main");
    await user.click(screen.getByRole("button", { name: "Add account" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("ABCD-1234")).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "Open the xAI sign-in page" })).toHaveAttribute("href", "https://accounts.example.invalid/device?user_code=ABCD-1234");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument(), { timeout: 5000 });
    expect(polls).toBeGreaterThanOrEqual(2);
    await user.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(patched).toHaveBeenCalledWith({ status: "paused" }));
  });

  it("forgets a sign-in when the dialog is closed and shows a refusal", async () => {
    const user = userEvent.setup();
    const cancelled = vi.fn();
    server.use(
      ...poolHandlers({ allowed: true }),
      http.post("/api/video/accounts/login", () =>
        HttpResponse.json({ flowId: "vlogin_2", userCode: "WXYZ-9876", verificationUrl: "https://accounts.example.invalid/device", intervalSeconds: 5, expiresInSeconds: 1800 }),
      ),
      http.get("/api/video/accounts/login/vlogin_2", () => HttpResponse.json({ status: "error", message: "xAI refused or expired this sign-in; start a new one" })),
      http.delete("/api/video/accounts/login/vlogin_2", () => {
        cancelled();
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderWithProviders(<VideoPage />);
    await screen.findAllByText("Main");
    await user.click(screen.getByRole("button", { name: "Add account" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("xAI refused or expired this sign-in; start a new one")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(cancelled).toHaveBeenCalledOnce());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows empty states without calling anything else", async () => {
    server.use(
      http.get("/api/video/accounts", () => HttpResponse.json({ accounts: [] })),
      http.get("/api/video/keys", () => HttpResponse.json({ keys: [] })),
      http.get("/api/video/jobs", () => HttpResponse.json({ jobs: [], nextCursor: null })),
      http.get("/api/video/summary", () => HttpResponse.json({ today: { ...totals, jobs: 0 }, week: totals, month: totals })),
    );
    renderWithProviders(<VideoPage />);
    expect(await screen.findByText("No Grok accounts yet")).toBeInTheDocument();
    expect(await screen.findByText("No key can use video generation yet.")).toBeInTheDocument();
  });
});
