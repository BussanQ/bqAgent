import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync("src/styles.css", "utf8");
function palette(selector: string): Record<string, string> {
  const start = css.indexOf(selector + " {");
  if (start < 0) throw new Error(`Missing theme: ${selector}`);
  const block = css.slice(start, css.indexOf("}", start));
  return Object.fromEntries([...block.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map(match => [match[1], match[2]]));
}

type RGB = [number, number, number];
function color(value: string, base: RGB = [255, 255, 255]): RGB {
  if (value.startsWith("#")) return [1, 3, 5].map(index => parseInt(value.slice(index, index + 2), 16)) as RGB;
  const channels = value.match(/[\d.]+/g)!.map(Number);
  const alpha = channels[3] ?? 1;
  return channels.slice(0, 3).map((channel, index) => channel * alpha + base[index] * (1 - alpha)) as RGB;
}
function luminance(rgb: RGB): number {
  const linear = rgb.map(channel => {
    const value = channel / 255;
    return value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4;
  });
  return linear[0] * .2126 + linear[1] * .7152 + linear[2] * .0722;
}
function contrast(first: RGB, second: RGB): number {
  const values = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (values[0] + .05) / (values[1] + .05);
}

describe("day theme transitions", () => {
  const light = palette(':root[data-theme="light"]');

  it("uses the same palette and gradients for system and explicit light mode", () => {
    expect(Object.keys(light).length).toBeGreaterThan(30);
    expect(palette(":root:not([data-theme])")).toEqual(light);
  });

  it("retains white panels and transitions gradually into the blue-white sky", () => {
    const sky = color(light["--cosmos-end"]);
    const stops = ["--shell-white", "--shell-mid", "--shell-edge"]
      .map(name => luminance(color(light[name], sky)));
    expect(stops[0]).toBeGreaterThan(.95);
    stops.push(luminance(sky));
    for (let index = 1; index < stops.length; index++) {
      expect(stops[index - 1] - stops[index]).toBeGreaterThan(0);
      expect(stops[index - 1] - stops[index]).toBeLessThan(.10);
    }
    expect(light["--workspace-surface"].replace("to left", "to right")).toBe(light["--conversation-surface"]);
  });

  it("keeps all text tiers readable on the white panels and transition surfaces", () => {
    const sky = color(light["--cosmos-end"]);
    for (const surface of ["--cosmos-end", "--shell-white", "--shell-mid", "--shell-edge", "--panel-strong", "--bubble-user"]) {
      for (const text of ["--text", "--text-soft", "--text-muted"]) {
        expect(contrast(color(light[text]), color(light[surface], sky)), `${text} on ${surface}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

it.each(["light", "dark"])("keeps dropdown labels and selection indicators legible in %s mode", theme => {
  const colors = palette(`:root[data-theme="${theme}"]`);
  for (const surface of ["--select-bg", "--select-active"]) {
    const background = color(colors[surface]);
    for (const label of ["--text", "--text-soft"]) {
      expect(contrast(color(colors[label]), background), label).toBeGreaterThanOrEqual(4.5);
    }
    expect(contrast(color(colors["--signal"]), background), "selection indicator").toBeGreaterThanOrEqual(3);
  }
});

it("draws a themed scrollbar thumb on both dropdown scrolling surfaces", () => {
  const sheet = new CSSStyleSheet();
  sheet.replaceSync(css);
  function styleRules(rules: CSSRuleList): CSSStyleRule[] {
    return Array.from(rules).flatMap(rule => {
      if (rule instanceof CSSStyleRule) return [rule];
      return "cssRules" in rule ? styleRules((rule as CSSGroupingRule).cssRules) : [];
    });
  }
  const styles = styleRules(sheet.cssRules);
  const styleFor = (selector: string) => styles.find(rule =>
    rule.selectorText.split(/,\s*/).includes(selector),
  )?.style;
  const thumb = "#model-select::part(listbox)::-webkit-scrollbar-thumb";
  expect(styleFor(thumb)?.background).toContain("var(--text-muted)");
  expect(styleFor(thumb)?.borderRadius).toBe("999px");
  expect(styleFor(thumb + ":active")?.backgroundColor).toBe("var(--signal)");
  const picker = styles.find(rule => rule.selectorText === ".select-field::picker(select)");
  expect(picker?.style.scrollbarWidth).toBe("thin");
  expect(picker?.style.scrollbarColor).toBe("var(--text-muted) transparent");
});
