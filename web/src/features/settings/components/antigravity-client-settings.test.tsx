import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { AntigravityClientSettings } from "@/features/settings/components/antigravity-client-settings";
import { buildSettingsUpdateRequest } from "@/features/settings/payload";
import { createDashboardSettings } from "@/test/mocks/factories";

describe("AntigravityClientSettings", () => {
  it("renders the saved version and explains active-connection behavior", () => {
    render(
      <AntigravityClientSettings
        settings={createDashboardSettings({ antigravityClientVersion: "2.34.0" })}
        busy={false}
        onSave={vi.fn().mockResolvedValue(undefined)}
      />,
    );

    expect(screen.getByLabelText("Antigravity client version")).toHaveValue("2.34.0");
    expect(
      screen.getByText(/next sign-in, model refresh and request/i),
    ).toBeInTheDocument();
  });

  it("saves only a trimmed valid version through the settings payload", async () => {
    const user = userEvent.setup();
    const settings = createDashboardSettings({
      version: 9,
      antigravityClientVersion: "2.34.0",
    });
    const onSave = vi.fn().mockResolvedValue(undefined);

    render(<AntigravityClientSettings settings={settings} busy={false} onSave={onSave} />);
    const input = screen.getByLabelText("Antigravity client version");
    await user.clear(input);
    await user.type(input, " 2.35.0 ");
    await user.click(screen.getByRole("button", { name: "Save version" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith(
      buildSettingsUpdateRequest(settings, { antigravityClientVersion: "2.35.0" }),
    );
  });

  it.each(["", "2.35", "v2.35.0", "2.35.0-beta.1"])("blocks invalid version %s", async (value) => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);

    render(
      <AntigravityClientSettings
        settings={createDashboardSettings({ antigravityClientVersion: "2.34.0" })}
        busy={false}
        onSave={onSave}
      />,
    );
    const input = screen.getByLabelText("Antigravity client version");
    await user.clear(input);
    if (value) {
      await user.type(input, value);
    }

    expect(screen.getByRole("button", { name: "Save version" })).toBeDisabled();
    expect(screen.getByText(/MAJOR.MINOR.PATCH format, for example/i)).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("disables controls while settings are busy", () => {
    render(
      <AntigravityClientSettings
        settings={createDashboardSettings({ antigravityClientVersion: "2.34.0" })}
        busy={true}
        onSave={vi.fn().mockResolvedValue(undefined)}
      />,
    );

    expect(screen.getByLabelText("Antigravity client version")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save version" })).toBeDisabled();
  });
});
