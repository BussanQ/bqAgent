import type { PendingFile, PendingImage } from "./types";

export const ATTACHMENT_LIMITS = {
  maxImages: 4,
  maxImageBytes: 3 * 1024 * 1024,
  maxTotalImageBytes: 8 * 1024 * 1024,
  maxFiles: 5,
  maxFileBytes: 2 * 1024 * 1024,
  maxTotalFileBytes: 6 * 1024 * 1024,
} as const;

export interface SentImage {
  mime_type: string;
  data_base64: string;
}

export interface SentFile {
  name?: string;
  path?: string;
  data_base64?: string;
}

export interface SentAttachments {
  images: SentImage[];
  files: SentFile[];
}

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

export function takePendingAttachmentsForSend(
  pendingImages: PendingImage[],
  pendingFiles: PendingFile[],
  revokeObjectURL: (url: string) => void = (url) => URL.revokeObjectURL(url),
): SentAttachments {
  const images = pendingImages
    .filter((image) => !image.loading && image.dataBase64)
    .map((image) => ({ mime_type: image.mimeType, data_base64: image.dataBase64 }));
  const files = pendingFiles
    .filter((file) => !file.loading)
    .map((file) => file.kind === "path"
      ? { path: file.path }
      : { name: file.name, data_base64: file.dataBase64 });

  pendingImages.forEach((image) => revokeObjectURL(image.objectURL));
  pendingImages.length = 0;
  pendingFiles.length = 0;
  return { images, files };
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
