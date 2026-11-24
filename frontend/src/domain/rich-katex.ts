import katex from 'katex';
import 'katex/dist/katex.min.css';
import DOMPurify from 'dompurify';
import { escapeHTML } from './markdown-hooks';

const MATH = [
  { expression: /\$\$([\s\S]+?)\$\$/g, display: true },
  { expression: /\\\[([\s\S]+?)\\\]/g, display: true },
  { expression: /\\\((.+?)\\\)/g, display: false },
];
export function decorateMath(root: HTMLElement): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  while (walker.nextNode()) {
    const node = walker.currentNode as Text;
    if (!node.parentElement?.closest('pre,code,textarea,script,style,.katex')) nodes.push(node);
  }
  for (const node of nodes) {
    let html = escapeHTML(node.data);
    let changed = false;
    for (const rule of MATH)
      html = html.replace(rule.expression, (all, expression: string) => {
        changed = true;
        try {
          return katex.renderToString(expression, {
            displayMode: rule.display,
            throwOnError: false,
            trust: false,
            strict: 'warn',
          });
        } catch {
          return escapeHTML(all);
        }
      });
    if (!changed) continue;
    const span = document.createElement('span');
    span.innerHTML = DOMPurify.sanitize(html, {
      ADD_ATTR: ['class', 'style', 'aria-hidden'],
      FORBID_TAGS: ['script', 'iframe', 'object', 'embed'],
    });
    node.replaceWith(...Array.from(span.childNodes));
  }
}
