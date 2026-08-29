import { describe, expect, it } from "vitest";
import { shouldGroupToolCalls } from "./tool-groups";

describe("工具调用分组", () => {
  it("从第三次工具调用开始分组", () => {
    expect(shouldGroupToolCalls(1)).toBe(false);
    expect(shouldGroupToolCalls(2)).toBe(false);
    expect(shouldGroupToolCalls(3)).toBe(true);
  });
});
