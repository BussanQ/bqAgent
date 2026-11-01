// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { ContextProgress } from "./context-progress";

const start = "正在压缩上下文…";
const done = "上下文已压缩（输入约 8000 tokens）";

describe("上下文压缩通知", () => {
  it("已有正文和群聊消息时仍显示独立通知，普通进度不会覆盖它", () => {
    const thread = document.createElement("div");
    thread.innerHTML = '<div class="reply">已有回复</div><div class="participant">群聊参与者回复</div><div class="tool">工具结果</div>';
    const progress = new ContextProgress(() => {
      const notice = document.createElement("div");
      thread.appendChild(notice);
      return notice;
    });
    expect(progress.apply(start)).toBe(true);
    expect(progress.apply(start)).toBe(true);
    expect(progress.apply("Calculating…")).toBe(false);
    expect(thread.querySelectorAll('[role="status"]')).toHaveLength(1);
    expect(thread.querySelector('[role="status"]')?.textContent).toBe(start);
    expect(progress.apply(done)).toBe(true);
    progress.finish();
    expect(thread.querySelector('[role="status"]')?.textContent).toBe(done);
    expect(thread.querySelector(".reply")?.textContent).toBe("已有回复");
    expect(thread.querySelector(".participant")?.textContent).toBe("群聊参与者回复");
    expect(thread.querySelector(".tool")?.textContent).toBe("工具结果");
  });

  it("回退裁剪结束等待，下一次压缩建立新通知", () => {
    const notices: HTMLElement[] = [];
    const progress = new ContextProgress(() => {
      const notice = document.createElement("div");
      notices.push(notice);
      return notice;
    });
    progress.apply(start);
    progress.apply("上下文已裁剪，继续处理");
    progress.finish();
    expect(notices[0].textContent).toBe("上下文已裁剪，继续处理");
    progress.apply(start);
    expect(notices).toHaveLength(2);
    progress.finish();
    expect(notices[1].textContent).toBe("上下文压缩已中断");
  });
});
