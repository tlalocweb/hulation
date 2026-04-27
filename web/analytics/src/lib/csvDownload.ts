// Trigger a browser-side download from a Blob + filename. Used by the
// ReportTable CSV button and anything else wanting a "save as" flow.

import { browser } from '$app/environment';

export function downloadBlob(blob: Blob, filename: string): void {
  if (!browser) return;
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Let the browser finish the download; revoke on next tick.
  setTimeout(() => URL.revokeObjectURL(url), 1_000);
}

export function csvFilename(report: string): string {
  const yyyymmdd = new Date().toISOString().slice(0, 10).replace(/-/g, '');
  return `${report}-${yyyymmdd}.csv`;
}
