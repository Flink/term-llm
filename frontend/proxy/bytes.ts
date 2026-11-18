const compactBytes = new Intl.NumberFormat('en-US', {
  notation: 'compact',
  maximumFractionDigits: 2,
});

export function byteValue(value: unknown, label: string): { text: string; title: string } {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0)
    return { text: '—', title: `${label}: not reported` };
  return {
    text: value < 1000 ? `${value} B` : `~${compactBytes.format(value)}`,
    title: `${label}: ${value.toLocaleString()} bytes`,
  };
}
