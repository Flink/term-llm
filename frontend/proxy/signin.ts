import { el } from './dom';

export const termLLMSite = 'https://term-llm.com/';
const proxyGuide = 'https://term-llm.com/guides/credential-proxy/';

/** Host shown to the user for a configured origin, e.g. llm.example.com. */
export function originHost(origin: string): string {
  try {
    return new URL(origin).host || origin;
  } catch {
    return origin;
  }
}

function logo(): HTMLAnchorElement {
  const mark = el('span', '>_');
  mark.className = 'proxy-logo-mark';
  mark.setAttribute('aria-hidden', 'true');
  const link = el('a', mark, 'term-llm');
  link.className = 'proxy-logo';
  link.href = termLLMSite;
  link.setAttribute('aria-label', 'term-llm website');
  return link;
}

/**
 * Signed-out layout: brand mark linking to term-llm.com, the status line (so
 * sign-in errors sit beside the form), and the sign-in card.
 */
export function signInShell(view: HTMLElement, status: HTMLElement): HTMLElement {
  view.classList.add('proxy-signin-card');
  const guide = el('a', 'Credential proxy');
  guide.href = proxyGuide;
  const foot = el('p', guide, ' by ', Object.assign(el('a', 'term-llm'), { href: termLLMSite }));
  foot.className = 'proxy-signin-foot';
  const shell = el('div', logo(), status, view, foot);
  shell.className = 'proxy-signin';
  return shell;
}
