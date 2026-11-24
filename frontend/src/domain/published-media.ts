export function publishedMediaKind(mime: string) {
  if (mime === 'text/html') return 'html';
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  if (mime.startsWith('image/')) return 'image';
  return undefined;
}

/** Published assets must stay on the authenticated, content-addressed media route. */
export function trustedPublishedMediaURL(value: string, kind?: 'audio' | 'html'): string {
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
    const extensions =
      kind === 'html'
        ? 'html'
        : kind === 'audio'
          ? 'mp3|wav|ogg|flac|m4a|weba'
          : 'png|jpe?g|gif|webp|bmp|mp4|webm|mp3|wav|ogg|flac|m4a|weba';
    return new RegExp(`/media/[a-f0-9]{32}\\.(?:${extensions})$`, 'i').test(url.pathname)
      ? value
      : '';
  } catch {
    return '';
  }
}
