function escapeLabel(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[character] ?? character);
}

export function iconMarkup(name: string, className = ""): string {
  return `<svg class="ui-icon${className ? ` ${className}` : ""}" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><use href="#icon-${name}"></use></svg>`;
}

export function createIcon(name: string, className = ""): SVGSVGElement {
  const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  icon.setAttribute("viewBox", "0 0 24 24");
  icon.setAttribute("aria-hidden", "true");
  icon.setAttribute("focusable", "false");
  icon.setAttribute("class", `ui-icon${className ? ` ${className}` : ""}`);
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", `#icon-${name}`);
  icon.appendChild(use);
  return icon;
}

export function setIconButtonLabel(button: HTMLButtonElement, iconName: string, label: string): void {
  button.innerHTML = `${iconMarkup(iconName)}<span>${escapeLabel(label)}</span>`;
}
