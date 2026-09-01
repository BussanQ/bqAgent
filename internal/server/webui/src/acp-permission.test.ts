import { describe, expect, it } from "vitest";
import { acpPermissionTitle, formatACPPermissionInput, isRejectPermissionOption } from "./acp-permission";

describe("ACP permission presentation", () => {
  it("uses the tool title and formats structured input", () => {
    expect(acpPermissionTitle({ toolCallId: "tool-1", title: "运行测试", kind: "execute" })).toBe("运行测试");
    expect(formatACPPermissionInput({ command: "go test ./internal/extagent" })).toContain("go test ./internal/extagent");
  });

  it("identifies reject options", () => {
    expect(isRejectPermissionOption("reject_once")).toBe(true);
    expect(isRejectPermissionOption("allow_once")).toBe(false);
  });
});
