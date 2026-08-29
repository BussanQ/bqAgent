export async function waitForModelSelection(selection: Promise<boolean> | null): Promise<boolean> {
  return selection ? await selection : true;
}
