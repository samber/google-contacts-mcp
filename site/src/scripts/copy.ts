export type CopyResult = 'copied' | 'unavailable';

type ClipboardLike = Pick<Clipboard, 'writeText'>;

/**
 * Writes text to the clipboard. Never throws: a missing API (insecure context,
 * old browser) and a refused write both report "unavailable", so the caller
 * can fall back to selecting the text for the user.
 */
export async function copyText(text: string, clipboard: ClipboardLike | undefined): Promise<CopyResult> {
  if (!clipboard) return 'unavailable';
  try {
    await clipboard.writeText(text);
    return 'copied';
  } catch {
    return 'unavailable';
  }
}
