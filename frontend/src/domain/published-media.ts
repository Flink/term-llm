export function publishedMediaKind(mime: string) {
  const kind = mime.slice(5);
  if (mime.startsWith('text/')) {
    if (kind === 'plain') return 'text';
    if (isDocumentMedia(kind) && kind !== 'text') return kind;
  }
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  if (mime.startsWith('image/')) return 'image';
  return undefined;
}

export function publishedMediaDownloadURL(url: string, name: string): string {
  return `${url}?download=${encodeURIComponent(name)}`;
}

export type DocumentMediaKind = 'html' | 'markdown' | 'text';

export function isDocumentMedia(kind?: string): kind is DocumentMediaKind {
  return kind === 'html' || kind === 'markdown' || kind === 'text';
}

/** Published assets must stay on the authenticated, content-addressed media route. */
export function trustedPublishedMediaURL(
  value: string,
  kind?: 'audio' | DocumentMediaKind,
): string {
  try {
    const url = new URL(value, location.href);
    if (
      !/^https?:$/.test(url.protocol) ||
      url.origin !== location.origin ||
      url.username ||
      url.password ||
      /[?#]/.test(value)
    )
      return '';
    const extensions = {
      html: 'html',
      markdown: 'md',
      text: 'txt',
      audio: 'mp3|wav|ogg|flac|m4a|weba',
      passive: 'png|jpe?g|gif|webp|bmp|mp4|webm|mp3|wav|ogg|flac|m4a|weba',
    }[kind || 'passive'];
    return new RegExp(`/media/[a-f0-9]{32}\\.(?:${extensions})$`, 'i').test(url.pathname)
      ? value
      : '';
  } catch {
    return '';
  }
}
