import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { createApiKey } from "@/test/mocks/factories";
import { renderWithProviders } from "@/test/utils";

import { ApiKeyEditDialog } from "./api-key-edit-dialog";

const fork = {
  parentId: "key_parent",
  parentName: "Project",
  createdBy: "owner" as const,
  updatedBy: "owner" as const,
  updatedAt: new Date().toISOString(),
};

describe("fork editor", () => {
  it("names the parent, hides the inherited settings and does not send them", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(
      <ApiKeyEditDialog open busy={false} apiKey={createApiKey({ fork })} onOpenChange={vi.fn()} onSubmit={onSubmit} />,
    );
    expect(screen.getByRole("note")).toHaveTextContent("Fork of Project");
    for (const label of ["Group", "Traffic class", "HTTP client routing"]) {
      expect(screen.queryByLabelText(label)).not.toBeInTheDocument();
    }
    expect(screen.queryByText("Assigned accounts")).not.toBeInTheDocument();
    expect(screen.getByText("Allowed models")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    const payload = onSubmit.mock.calls[0][0];
    for (const field of ["groupId", "trafficClass", "transportPolicyOverride", "usageSections", "assignedAccountIds", "assignedSourceIds"]) {
      expect(field in payload, field).toBe(false);
    }
  });

  it("keeps the full editor for a key that is not a fork", () => {
    renderWithProviders(<ApiKeyEditDialog open busy={false} apiKey={createApiKey()} onOpenChange={vi.fn()} onSubmit={vi.fn()} />);
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Group")).toBeInTheDocument();
  });
});
