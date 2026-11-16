import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { createApiKey } from "@/test/mocks/factories";
import { renderWithProviders } from "@/test/utils";

import { ApiKeyCreateDialog } from "./api-key-create-dialog";
import { ApiKeyEditDialog } from "./api-key-edit-dialog";
import { parseRequestPool } from "./request-pool-utils";

describe("request pool editor", () => {
  it("parses only pools in range", () => {
    expect(parseRequestPool({ enabled: false, requestsPerMinute: "", maxWaitSeconds: "30" })).toBeNull();
    expect(parseRequestPool({ enabled: true, requestsPerMinute: "120", maxWaitSeconds: "30" })).toEqual({ requestsPerMinute: 120, maxWaitSeconds: 30 });
    expect(parseRequestPool({ enabled: true, requestsPerMinute: "1", maxWaitSeconds: "0" })).toEqual({ requestsPerMinute: 1, maxWaitSeconds: 0 });
    for (const [requestsPerMinute, maxWaitSeconds] of [["0", "30"], ["10001", "30"], ["5", "61"], ["", "30"], ["2.5", "30"], ["5", "-1"]]) {
      expect(parseRequestPool({ enabled: true, requestsPerMinute, maxWaitSeconds })).toBeUndefined();
    }
  });

  it("sets a pool on a key and keeps it out of unrelated edits", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(
      <ApiKeyEditDialog open busy={false} apiKey={createApiKey({ requestPool: null })} onOpenChange={vi.fn()} onSubmit={onSubmit} />,
    );
    await user.click(screen.getByRole("switch", { name: "Request pool" }));
    await user.type(screen.getByLabelText("Requests per minute"), "120");
    expect(screen.getByLabelText("Maximum wait, seconds")).toHaveValue("30");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0][0].requestPool).toEqual({ requestsPerMinute: 120, maxWaitSeconds: 30 });
  });

  it("clears a pool and refuses an invalid one", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const apiKey = createApiKey({ requestPool: { requestsPerMinute: 60, maxWaitSeconds: 10 } });
    const { unmount } = renderWithProviders(
      <ApiKeyEditDialog open busy={false} apiKey={apiKey} onOpenChange={vi.fn()} onSubmit={onSubmit} />,
    );
    const rpm = screen.getByLabelText("Requests per minute");
    await user.clear(rpm);
    await user.type(rpm, "0");
    expect(screen.getByRole("alert")).toHaveTextContent("1-10000");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSubmit).not.toHaveBeenCalled();
    await user.click(screen.getByRole("switch", { name: "Request pool" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0][0].requestPool).toBeNull();
    unmount();

    // An unchanged pool is not sent again.
    const untouched = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(<ApiKeyEditDialog open busy={false} apiKey={apiKey} onOpenChange={vi.fn()} onSubmit={untouched} />);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(untouched).toHaveBeenCalledTimes(1));
    expect("requestPool" in untouched.mock.calls[0][0]).toBe(false);
  });

  it("creates a key with a pool", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(<ApiKeyCreateDialog open busy={false} onOpenChange={vi.fn()} onSubmit={onSubmit} />);
    await user.type(screen.getByLabelText("Name"), "Project");
    await user.click(screen.getByRole("switch", { name: "Request pool" }));
    await user.type(screen.getByLabelText("Requests per minute"), "120");
    const wait = screen.getByLabelText("Maximum wait, seconds");
    await user.clear(wait);
    await user.type(wait, "0");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0][0].requestPool).toEqual({ requestsPerMinute: 120, maxWaitSeconds: 0 });
  });
});
