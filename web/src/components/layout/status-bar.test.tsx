import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { StatusBar, type StatusBarProps } from "@/components/layout/status-bar";
import i18n from "@/i18n";
import { useDateDisplayFormatStore } from "@/hooks/use-date-format";
import { formatTimeLong } from "@/utils/formatters";
import { server } from "@/test/mocks/server";
import { createShellStatus } from "@/test/mocks/overview-factories";

function mockShell(overrides: Parameters<typeof createShellStatus>[0] = {}) {
  server.use(http.get("/api/dashboard/status", () => HttpResponse.json(createShellStatus(overrides))));
}

function renderStatusBar(props: StatusBarProps = {}, path = "/settings") {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <StatusBar {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const mockSettings = mockShell;

describe("StatusBar", () => {
  it("shows ready service independently from stale usage", async () => {
    mockShell({ ready: true, lastSyncAt: new Date(Date.now() - 120_000).toISOString() });

    renderStatusBar();

    expect(await screen.findByText("Ready")).toBeInTheDocument();
    expect(await screen.findByText("Stale")).toBeInTheDocument();
  });

  it("reads the shell status, never the readiness probe, the settings or a page endpoint", async () => {
    const requests: string[] = [];
    server.events.on("request:start", ({ request }) => {
      requests.push(new URL(request.url).pathname);
    });
    mockShell({ lastSyncAt: new Date().toISOString() });

    renderStatusBar();

    expect(await screen.findByText("Synced")).toBeInTheDocument();
    server.events.removeAllListeners("request:start");
    expect(requests).toContain("/api/dashboard/status");
    expect(requests).not.toContain("/health/ready");
    expect(requests).not.toContain("/api/settings");
    expect(requests).not.toContain("/api/dashboard/overview");
    expect(requests).not.toContain("/api/accounts");
  });

  it("does not request the status on a page whose poll carries it", async () => {
    let statusRequests = 0;
    server.use(
      http.get("/api/dashboard/status", () => {
        statusRequests += 1;
        return HttpResponse.json(createShellStatus());
      }),
    );

    renderStatusBar({}, "/overview");

    expect(await screen.findByText("Checking")).toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(statusRequests).toBe(0);
  });

  it("shows fresh usage independently from an unready service", async () => {
    mockShell({ ready: false, lastSyncAt: new Date().toISOString() });

    renderStatusBar();

    expect(await screen.findByText("Not ready")).toBeInTheDocument();
    expect(await screen.findByText("Synced")).toBeInTheDocument();
  });

  it("shows the fold state of the usage rollups", async () => {
    mockShell({ rollupsUsable: false });

    renderStatusBar();

    expect(await screen.findByText("catching up")).toBeInTheDocument();
  });

  it("updates the last sync clock when the date format changes", async () => {
    const lastSyncAt = "2026-08-09T14:30:45.000Z";
    mockShell({ lastSyncAt });
    useDateDisplayFormatStore.setState({ dateDisplayFormat: "default" });

    renderStatusBar();

    const defaultTime = formatTimeLong(lastSyncAt, "default").time;
    const footer = screen.getByRole("contentinfo");
    await screen.findByText("Ready");
    expect(footer).toHaveTextContent(defaultTime);

    act(() => {
      useDateDisplayFormatStore.setState({ dateDisplayFormat: "iso8601" });
    });

    const isoTime = formatTimeLong(lastSyncAt, "iso8601").time;
    expect(footer).toHaveTextContent(isoTime);
  });

  it("shows checking while the shell status is pending", async () => {
    let resolveStatus!: () => void;
    const pending = new Promise<void>((resolve) => {
      resolveStatus = resolve;
    });
    server.use(
      http.get("/api/dashboard/status", async () => {
        await pending;
        return HttpResponse.json(createShellStatus({ lastSyncAt: new Date().toISOString() }));
      }),
    );

    renderStatusBar();

    expect(await screen.findByText("Checking")).toBeInTheDocument();

    await act(async () => {
      resolveStatus();
    });
    expect(await screen.findByText("Ready")).toBeInTheDocument();
    expect(await screen.findByText("Synced")).toBeInTheDocument();
  });

  it("reports its resized height so wrapped rows remain clear of page content", () => {
    let resizeCallback: ResizeObserverCallback | undefined;
    const observe = vi.fn();
    const disconnect = vi.fn();
    const offsetHeight = vi
      .spyOn(HTMLElement.prototype, "offsetHeight", "get")
      .mockReturnValue(40);
    const resizeObserver = vi
      .spyOn(globalThis, "ResizeObserver")
      .mockImplementation(
        class {
          constructor(callback: ResizeObserverCallback) {
            resizeCallback = callback;
          }

          observe = observe;
          unobserve = vi.fn();
          disconnect = disconnect;
        },
      );
    const onHeightChange = vi.fn();

    try {
      const { unmount } = renderStatusBar({ onHeightChange });

      expect(onHeightChange).toHaveBeenLastCalledWith(40);
      expect(observe).toHaveBeenCalledWith(screen.getByRole("contentinfo"));

      offsetHeight.mockReturnValue(72);
      act(() => {
        resizeCallback?.([], {} as ResizeObserver);
      });

      expect(onHeightChange).toHaveBeenLastCalledWith(72);

      unmount();
      expect(disconnect).toHaveBeenCalledOnce();
    } finally {
      resizeObserver.mockRestore();
      offsetHeight.mockRestore();
    }
  });

  it("links to the official GitHub repository", () => {
    renderStatusBar();

    const link = screen.getByRole("link", { name: "Open official GitHub repository" });

    expect(link).toHaveAttribute("href", "https://github.com/Dirard/codex-lb");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
  });

  it("links to release notes when a newer version is available", async () => {
    server.use(
      http.get("/api/runtime/version", () =>
        HttpResponse.json({
          currentVersion: "1.19.0",
          latestVersion: "1.20.0",
          updateAvailable: true,
          checkedAt: "2026-05-26T00:00:00Z",
          source: "github",
          releaseUrl: "https://github.com/Soju06/codex-lb/releases/latest",
        }),
      ),
    );

    renderStatusBar();

    const link = await screen.findByRole("link", {
      name: "New version available: 1.20.0. Open release notes.",
    });

    expect(link).toHaveAttribute("href", "https://github.com/Soju06/codex-lb/releases/latest");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
  });

  it("does not show an update link when the runtime version check fails", async () => {
    server.use(
      http.get("/api/runtime/version", () =>
        HttpResponse.json({ error: "upstream unavailable" }, { status: 503 }),
      ),
    );

    renderStatusBar();

    expect(await screen.findByText("Version:")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", {
        name: /New version available/,
      }),
    ).not.toBeInTheDocument();
  });

  it("localizes combined routing labels in zh-CN", async () => {
    await i18n.changeLanguage("zh-CN");
    try {
      mockSettings({
        routingStrategy: "capacity_weighted",
        stickyThreadsEnabled: true,
        preferEarlierResetAccounts: true,
        preferEarlierResetWindow: "secondary",
      });

      renderStatusBar();

      expect(await screen.findByText(/按容量加权/)).toBeInTheDocument();
      expect(screen.getByText(/粘性/)).toBeInTheDocument();
      expect(screen.getByText(/较早周重置/)).toBeInTheDocument();
      expect(screen.queryByText(/Capacity weighted/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Sticky threads/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Early weekly reset/)).not.toBeInTheDocument();
    } finally {
      await i18n.changeLanguage("en");
    }
  });

  it("does not show early reset for strategies that do not honor it", async () => {
    mockSettings({
      routingStrategy: "round_robin",
      stickyThreadsEnabled: false,
      preferEarlierResetAccounts: true,
      preferEarlierResetWindow: "secondary",
    });

    renderStatusBar();

    expect(await screen.findByText("Round robin")).toBeInTheDocument();
    expect(screen.queryByText("Early weekly reset")).not.toBeInTheDocument();
    expect(screen.queryByText("Round robin + Early weekly reset")).not.toBeInTheDocument();
  });

  it("keeps single-account routing label unadorned", async () => {
    mockSettings({
      routingStrategy: "single_account",
      stickyThreadsEnabled: true,
      preferEarlierResetAccounts: true,
      preferEarlierResetWindow: "secondary",
    });

    renderStatusBar();

    expect(await screen.findByText("Single account")).toBeInTheDocument();
    expect(screen.queryByText("Single account + Sticky threads")).not.toBeInTheDocument();
    expect(screen.queryByText("Single account + Sticky + Early weekly reset")).not.toBeInTheDocument();
  });

  it("still shows early reset for supported strategies", async () => {
    mockSettings({
      routingStrategy: "fill_first",
      stickyThreadsEnabled: false,
      preferEarlierResetAccounts: true,
      preferEarlierResetWindow: "secondary",
    });

    renderStatusBar();

    expect(await screen.findByText("Fill first + Early weekly reset")).toBeInTheDocument();
  });
});
