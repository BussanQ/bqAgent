export const ATTACHMENT_LIMITS = {
  maxImages: 4,
  maxImageBytes: 3 * 1024 * 1024,
  maxTotalImageBytes: 8 * 1024 * 1024,
  maxFiles: 5,
  maxFileBytes: 2 * 1024 * 1024,
  maxTotalFileBytes: 6 * 1024 * 1024,
} as const;

const ALLOWED_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif"]);

export function validateImageAttachment(count: number, totalBytes: number, file: Pick<File, "type" | "size">): string {
  if (count >= ATTACHMENT_LIMITS.maxImages) return "每次最多发送 4 张图片";
  if (!ALLOWED_IMAGE_TYPES.has(file.type)) return "仅支持 PNG、JPEG 和 GIF 图片";
  if (file.size > ATTACHMENT_LIMITS.maxImageBytes) return "单张图片不能超过 3 MiB";
  if (totalBytes + file.size > ATTACHMENT_LIMITS.maxTotalImageBytes) return "图片总大小不能超过 8 MiB";
  return "";
}

export function validateFileAttachment(count: number, totalBytes: number, size: number): string {
  if (count >= ATTACHMENT_LIMITS.maxFiles) return "每次最多添加 5 个文件";
  if (size > ATTACHMENT_LIMITS.maxFileBytes) return "单个文件不能超过 2 MiB";
  if (totalBytes + size > ATTACHMENT_LIMITS.maxTotalFileBytes) return "文件总大小不能超过 6 MiB";
  return "";
}

export function showTemporaryError(
  element: HTMLElement,
  message: string,
  timeoutMs: number,
  onExpired: () => void,
): ReturnType<typeof setTimeout> | 0 {
  element.textContent = message;
  if (!message) return 0;
  return setTimeout(() => {
    element.textContent = "";
    onExpired();
  }, timeoutMs);
}
