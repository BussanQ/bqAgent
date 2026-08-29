export const TOOL_GROUP_START_COUNT = 3;

export function shouldGroupToolCalls(count: number): boolean {
  return count >= TOOL_GROUP_START_COUNT;
}
