export function byId<T extends HTMLElement>(id: string): T {
  const element = document.getElementById(id);
  if (!element) throw new Error(`WebUI element #${id} is missing`);
  return element as T;
}

export function query<T extends Element>(selector: string): T {
  const element = document.querySelector(selector);
  if (!element) throw new Error(`WebUI element ${selector} is missing`);
  return element as T;
}

export function eventElement(target: EventTarget | null): Element | null {
  return target instanceof Element ? target : null;
}
