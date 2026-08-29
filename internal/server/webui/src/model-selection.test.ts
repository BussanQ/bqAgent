import { describe, expect, it } from "vitest";
import { waitForModelSelection } from "./model-selection";

describe("模型切换等待", () => {
  it("发送前等待正在进行的模型切换", async () => {
    let finishSelection: (selected: boolean) => void = () => undefined;
    const selection = new Promise<boolean>((resolve) => { finishSelection = resolve; });
    let settled = false;
    const waiting = waitForModelSelection(selection).then((value) => { settled = true; return value; });
    await Promise.resolve();
    expect(settled).toBe(false);
    finishSelection(true);
    await expect(waiting).resolves.toBe(true);
  });

  it("没有切换任务时立即继续，失败选择则阻止发送", async () => {
    await expect(waitForModelSelection(null)).resolves.toBe(true);
    await expect(waitForModelSelection(Promise.resolve(false))).resolves.toBe(false);
  });
});
