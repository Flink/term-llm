export const DIFF_SCOPES = [
  'last_turn',
  'last_3_turns',
  'all_turns',
  'uncommitted',
  'unstaged',
  'staged',
] as const;

export type DiffScope = (typeof DIFF_SCOPES)[number];

/**
 * Scope normalization is shared by eager review queueing and deferred diff
 * parsing. Keep it in this dependency-free module so review-store.ts can
 * normalize synchronously without pulling domain/diff.ts parsers into the
 * initial bundle.
 */
export function normalizeDiffScope(value: unknown): DiffScope {
  const scope = String(value || '')
    .trim()
    .toLowerCase()
    .replaceAll('-', '_');
  return DIFF_SCOPES.includes(scope as DiffScope) ? (scope as DiffScope) : 'last_turn';
}

/**
 * The scope a Changes view keeps when its owning conversation changes.
 * All-turns history belongs to one conversation, so selecting another chat
 * falls back to the last turn; every other scope stays valid across chats.
 */
export function diffScopeForOwner(scope: unknown, ownerChanged: boolean): DiffScope {
  const normalized = normalizeDiffScope(scope);
  return normalized === 'all_turns' && ownerChanged ? 'last_turn' : normalized;
}
