import { useEffect, useMemo, useState } from 'preact/hooks';
import type { Completion } from '../domain/completions';
import { loadEmojiCatalog, type EmojiCatalog } from '../domain/emoji-catalog';
import { activeEmojiAtCaret, indexEmoji, searchEmoji } from '../domain/emoji-completion';

export function useEmojiCompletion(value: string, owner: string) {
  const [selection, setSelection] = useState<{
    value: string;
    caret: number;
    owner: string;
  } | null>(null);
  const [composing, setComposing] = useState(false);
  const [catalog, setCatalog] = useState<EmojiCatalog | null>(null);
  const caret = selection?.owner === owner && selection.value === value ? selection.caret : null;
  const token = caret !== null && !composing ? activeEmojiAtCaret(value, caret) : null;
  const eligible = token !== null;

  useEffect(() => {
    if (!eligible || catalog) return;
    let cancelled = false;
    // A missing chunk (e.g. after a server upgrade) must not break the composer.
    void loadEmojiCatalog().then(
      (rows) => !cancelled && setCatalog(rows),
      () => !cancelled && setCatalog([]),
    );
    return () => {
      cancelled = true;
    };
  }, [eligible, catalog]);

  const index = useMemo(() => (catalog ? indexEmoji(catalog) : null), [catalog]);
  const query = token?.query;
  const matches = useMemo(() => (index && query ? searchEmoji(index, query) : []), [index, query]);
  const items: Completion[] = matches.map(({ glyph, shortcode }) => ({
    kind: 'emoji',
    value: glyph,
    label: glyph,
    description: `:${shortcode}:`,
    replacement: token ?? undefined,
  }));

  const sync = (input: HTMLTextAreaElement) => {
    const caret = input.selectionStart;
    const next =
      input.ownerDocument.activeElement === input && caret === input.selectionEnd
        ? { value: input.value, caret, owner }
        : null;
    if (
      selection?.value === next?.value &&
      selection?.caret === next?.caret &&
      selection?.owner === next?.owner
    )
      return false;
    setSelection(next);
    return true;
  };

  return {
    items,
    caret,
    pending: eligible && !catalog,
    sync,
    close: () => setSelection(null),
    setComposing,
  };
}
