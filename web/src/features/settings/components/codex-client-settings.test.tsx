import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { CodexClientSettings } from "@/features/settings/components/codex-client-settings";
import { buildSettingsUpdateRequest } from "@/features/settings/payload";
import { createDashboardSettings } from "@/test/mocks/factories";

describe("CodexClientSettings", () => {
  it("renders the saved version and explains active-connection behavior", () => {
    render(
      <CodexClientSettings
        settings={createDashboardSettings({ codexClientVersion: "0.156.0" })}
        busy={false}
        onSave={vi.fn().mockResolvedValue(undefined)}
      />,
    );

    expect(screen.getByLabelText("Codex client version")).toHaveValue("0.156.0");
    expect(
      screen.getByText(/existing streams keep their established version/i),
    ).toBeInTheDocument();
  });

  it("saves only a trimmed valid version through the settings payload", async () => {
    const user = userEvent.setup();
    const settings = createDashboardSettings({
      version: 9,
      codexClientVersion: "0.156.0",
    });
    const onSave = vi.fn().mockResolvedValue(undefined);

    render(<CodexClientSettings settings={settings} busy={false} onSave={onSave} />);
    const input = screen.getByLabelText("Codex client version");
    await user.clear(input);
    await user.type(input, " 0.157.0-beta.1 ");
    await user.click(screen.getByRole("button", { name: "Save version" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith(
      buildSettingsUpdateRequest(settings, { codexClientVersion: "0.157.0-beta.1" }),
    );
  });

  it.each(["", "0.156", "v0.156.0"])("blocks invalid version %s", async (value) => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);

    render(
      <CodexClientSettings
        settings={createDashboardSettings({ codexClientVersion: "0.156.0" })}
        busy={false}
        onSave={onSave}
      />,
    );
    const input = screen.getByLabelText("Codex client version");
    await user.clear(input);
    if (value) {
      await user.type(input, value);
    }

    expect(screen.getByRole("button", { name: "Save version" })).toBeDisabled();
    expect(screen.getByText(/Enter a non-empty version/i)).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("disables controls while settings are busy", () => {
    render(
      <CodexClientSettings
        settings={createDashboardSettings({ codexClientVersion: "0.156.0" })}
        busy={true}
        onSave={vi.fn().mockResolvedValue(undefined)}
      />,
    );

    expect(screen.getByLabelText("Codex client version")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save version" })).toBeDisabled();
  });
});
