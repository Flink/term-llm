export type EmojiCatalog = readonly (readonly [
  glyph: string,
  name: string,
  keywords: string,
  aliases?: string,
])[];

let catalog: Promise<EmojiCatalog> | undefined;

export function loadEmojiCatalog(): Promise<EmojiCatalog> {
  return (catalog ??= import('../../../internal/emoji/catalog.json').then(
    ({ default: rows }) => rows as [string, string, string, string?][],
  ));
}
