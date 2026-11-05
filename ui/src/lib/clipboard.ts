/** Copies text to the clipboard. Resolves false when the browser refuses (the user can still select the text). */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
