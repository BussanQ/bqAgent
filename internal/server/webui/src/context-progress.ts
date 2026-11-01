// Compaction has its own notice so it never replaces assistant text or tool cards.
export class ContextProgress {
  private notice: HTMLElement | null = null;
  private active = false;

  constructor(private readonly createNotice: () => HTMLElement) {}

  apply(message: string): boolean {
    const text = message.trim();
    const starting = text === "正在压缩上下文…";
    const completed = text.startsWith("上下文已压缩") || text === "上下文已裁剪，继续处理";
    if (!starting && !completed) return false;
    if (!this.notice || (starting && !this.active)) {
      this.notice = this.createNotice();
      this.notice.classList.add("notice");
      this.notice.setAttribute("role", "status");
    }
    this.notice.textContent = text;
    this.active = starting;
    return true;
  }

  finish(): void {
    if (this.active && this.notice) this.notice.textContent = "上下文压缩已中断";
    this.active = false;
  }
}
