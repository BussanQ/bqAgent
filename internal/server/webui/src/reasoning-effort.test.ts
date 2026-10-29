import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { DEFAULT_REASONING_EFFORT, normalizeReasoningEffort, REASONING_EFFORT_LABELS, REASONING_EFFORT_VALUES } from "./reasoning-effort";

describe("reasoning effort", () => {
  it("restores max and existing preferences and defaults invalid values to auto", () => {
    for (const value of ["auto", "low", "medium", "high", "max"]) {
      expect(normalizeReasoningEffort(value)).toBe(value);
    }
    expect(normalizeReasoningEffort(" MAX ")).toBe("max");
    expect(normalizeReasoningEffort(undefined)).toBe("auto");
    expect(normalizeReasoningEffort("invalid")).toBe("auto");
  });

  it("defaults new browsers to high and renders the slider at high", () => {
    expect(DEFAULT_REASONING_EFFORT).toBe("high");
    const html = readFileSync("index.html", "utf8");
    document.body.innerHTML = html.slice(html.indexOf("<body"), html.indexOf("</body>") + 7);
    const range = document.getElementById("reasoning-effort-range") as HTMLInputElement;
    expect(REASONING_EFFORT_VALUES[Number(range.value)]).toBe("high");
    expect(document.getElementById("reasoning-effort-label")!.textContent).toBe(REASONING_EFFORT_LABELS.high);
  });

  it("makes max reachable at the slider endpoint with matching labels and ticks", () => {
    const html = readFileSync("index.html", "utf8");
    document.body.innerHTML = html.slice(html.indexOf("<body"), html.indexOf("</body>") + 7);
    const range = document.getElementById("reasoning-effort-range") as HTMLInputElement;
    range.value = range.max;
    expect(REASONING_EFFORT_VALUES[Number(range.value)]).toBe("max");
    const labels = document.getElementById("reasoning-effort-labels")!;
    expect(Array.from(labels.children, item => item.textContent)).toEqual(REASONING_EFFORT_VALUES.map(value => REASONING_EFFORT_LABELS[value]));
    expect(document.querySelectorAll(".reasoning-effort-dots span")).toHaveLength(REASONING_EFFORT_VALUES.length);
  });
});
