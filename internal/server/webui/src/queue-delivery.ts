import type { ConversationMessage } from "./types";

export function renderQueueDelivery(bubble: HTMLElement, message: ConversationMessage, url: (id: string, index: number) => string): void {
  const delivery = message.delivery;
  if (!delivery) return;
  bubble.dataset.queueDelivery = delivery.id;
  bubble.replaceChildren();
  if (message.content) {
    const text = document.createElement("div");
    text.className = "user-message-text";
    text.textContent = message.content;
    bubble.append(text);
  }
  (delivery.attachments || []).forEach((file, index) => {
    const link = document.createElement("a");
    link.href = url(delivery.id, index);
    link.target = "_blank";
    link.rel = "noopener";
    if (file.kind === "image") {
      const image = document.createElement("img");
      image.loading = "lazy";
      image.alt = "已发送图片";
      image.src = link.href;
      link.className = "user-images";
      link.append(image);
    } else {
      link.textContent = file.name || file.path || "文件";
    }
    bubble.append(link);
  });
  const status = document.createElement("div");
  status.className = "queue-delivery-status";
  status.textContent = delivery.state === "returned" ? "Agent 已退回" : "执行结果未确认";
  bubble.append(status);
}
