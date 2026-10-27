import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { PasswordConfirmDialog } from "@/components/password-confirm-dialog";

describe("PasswordConfirmDialog", () => {
  it("submits the entered password only once it is non-empty", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <PasswordConfirmDialog
        open
        title="Export account tokens"
        description="Confirm your dashboard password to continue."
        confirmLabel="Export"
        onConfirm={onConfirm}
        onOpenChange={onOpenChange}
      />,
    );
    const submit = screen.getByRole("button", { name: "Export" });
    expect(submit).toBeDisabled();
    await user.type(screen.getByLabelText("Dashboard password"), "synthetic-password");
    await user.click(submit);
    expect(onConfirm).toHaveBeenCalledWith("synthetic-password");
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
