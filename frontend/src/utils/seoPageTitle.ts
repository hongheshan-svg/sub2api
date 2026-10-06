/**
 * SEO landing pages (SeoLandingView) own their full, keyword-rich <title>.
 * The generic route title resolver consults this registry first, so the
 * App / router / locale title refreshes don't overwrite it with the generic
 * "<meta.title> - <site name>" form — which is what crawlers would otherwise
 * index after rendering.
 */
const titles = new Map<string, string>()

export function setSeoPageTitle(path: string, title: string): void {
  if (path && title) {
    titles.set(path, title)
  }
}

export function getSeoPageTitle(path: string | undefined): string | undefined {
  return path ? titles.get(path) : undefined
}

// Seed from the server-injected landing page so the very first title
// resolution (router guard, before the view mounts) already matches the HTML.
if (typeof window !== 'undefined') {
  const seeded = (window as { __SEO_PAGE__?: { path?: string; title?: string } }).__SEO_PAGE__
  if (seeded?.path && seeded.title) {
    setSeoPageTitle(seeded.path, seeded.title)
  }
}
