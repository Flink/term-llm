import { useEffect, useMemo, useState } from 'preact/hooks';
import type { Completion } from '../domain/completions';
import { activeEmojiAtCaret, loadEmojiSearch } from '../domain/emoji-completion';
import type { EmojiSearch } from '../domain/emoji-search';

export function useEmojiCompletion(value: string, owner: string) {
  const [selection, setSelection] = useState<{
    value: string;
    caret: number;
    owner: string;
  } | null>(null);
  const [search, setSearch] = useState<EmojiSearch | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const caret = selection?.owner === owner && selection.value === value ? selection.caret : null;
  // Start fetching the catalog at the colon; suggest from the second character.
  // Composition is not excluded: Android keyboards compose every typed word,
  // and keyboard acceptance is already ignored until composition ends.
  const lead = caret !== null ? activeEmojiAtCaret(value, caret, 0) : null;
  const token = lead && lead.query.length >= 2 ? lead : null;
  const leadKey = lead ? `${owner}\u0000${lead.start}` : null;

  useEffect(() => {
    if (!leadKey || search) return;
    let cancelled = false;
    // A missing chunk (e.g. after a server upgrade) must not break the composer:
    // release this shortcode's Enter guard and let the next shortcode retry.
    void loadEmojiSearch().then(
      (loaded) => !cancelled && setSearch(() => loaded),
      () => !cancelled && setFailed(leadKey),
    );
    return () => {
      cancelled = true;
    };
  }, [leadKey, search]);

  const query = token?.query;
  const matches = useMemo(() => (search && query ? search(query) : []), [search, query]);
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
    pending: token !== null && !search && failed !== leadKey,
    sync,
    close: () => setSelection(null),
  };
}
