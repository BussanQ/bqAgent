export function complexTaskNotice(text: string): string {
  const match = String(text || "").match(/^Agent stopped: reached maximum of (\d+) iterations without completing\.$/);
  if (!match) return "";
  return [
    "复杂任务还没有完成。",
    "",
    `本轮已触达失控保险上限（${match[1]} 次迭代）。默认已开启自动压缩续跑，正常很难碰到这个上限；你可以继续发送“继续”，我会沿当前会话接着处理。如确实需要更长的单轮，可在 \`.env\` 中调大 \`AGENT_MAX_ITERATIONS\` 后重启服务。`,
  ].join("\n");
}
