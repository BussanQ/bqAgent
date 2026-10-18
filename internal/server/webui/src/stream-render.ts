// Coalesce text updates by bubble, preserving the user's position while reading
// earlier messages. Terminal events flush before rendering their final state.
export class StreamRenderQueue {
  private frame: number | null = null;
  private pending = new Map<object, () => void>();
  private following: boolean;
  private disposed = false;
  private readonly onScroll = () => { this.following = this.nearBottom(); };

  constructor(
    private readonly scroller: HTMLElement,
    private readonly requestFrame: (callback: FrameRequestCallback) => number = callback => window.requestAnimationFrame(callback),
    private readonly cancelFrame: (id: number) => void = id => window.cancelAnimationFrame(id),
  ) {
    this.following = this.nearBottom();
    scroller.addEventListener("scroll", this.onScroll, { passive: true });
  }

  private nearBottom(): boolean {
    return this.scroller.scrollHeight - this.scroller.scrollTop - this.scroller.clientHeight <= 80;
  }

  schedule(key?: object, render?: () => void): void {
    if (this.disposed) return;
    if (key && render) this.pending.set(key, render);
    if (this.frame === null) this.frame = this.requestFrame(() => {
      this.frame = null;
      this.flush();
    });
  }

  flush(): void {
    if (this.disposed) return;
    if (this.frame !== null) this.cancelFrame(this.frame);
    this.frame = null;
    const pending = Array.from(this.pending.values());
    this.pending.clear();
    for (const render of pending) render();
    if (this.following) this.scroller.scrollTop = this.scroller.scrollHeight;
  }

  dispose(): void {
    if (this.frame !== null) this.cancelFrame(this.frame);
    this.frame = null;
    this.pending.clear();
    this.scroller.removeEventListener("scroll", this.onScroll);
    this.disposed = true;
  }
}
