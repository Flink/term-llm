import '../styles/features/tool-arguments.css';

function parseToolArguments(
  raw: string,
): { entries: [string, unknown][]; fallback: string } | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed))
      return { entries: [], fallback: raw };
    return { entries: Object.entries(parsed as Record<string, unknown>), fallback: '' };
  } catch {
    return { entries: [], fallback: raw };
  }
}

function ToolArgumentValue({ value }: { value: unknown }) {
  if (value == null || typeof value === 'boolean' || typeof value === 'number')
    return <code class="tool-argument-literal">{value == null ? 'null' : String(value)}</code>;
  if (typeof value === 'string')
    return (
      <span class={`tool-argument-text ${value.includes('\n') ? 'multiline' : ''}`}>{value}</span>
    );
  const compact = JSON.stringify(value) || String(value);
  const formatted = compact.length > 100 ? JSON.stringify(value, null, 2) : compact;
  return (
    <code class={`tool-argument-structured ${compact.length > 100 ? 'multiline' : ''}`}>
      {formatted}
    </code>
  );
}

export function ToolArguments({ raw }: { raw: string }) {
  const parsed = parseToolArguments(raw);
  if (!parsed || (!parsed.entries.length && !parsed.fallback)) return null;
  return (
    <>
      <div class="tool-details-label">Arguments</div>
      {parsed.entries.length ? (
        <dl class="tool-arguments">
          {parsed.entries.map(([name, value]) => (
            <div class="tool-argument" key={name}>
              <dt>{name}</dt>
              <dd>
                <ToolArgumentValue value={value} />
              </dd>
            </div>
          ))}
        </dl>
      ) : (
        <pre class="tool-arguments-fallback">
          <code>{parsed.fallback}</code>
        </pre>
      )}
    </>
  );
}
