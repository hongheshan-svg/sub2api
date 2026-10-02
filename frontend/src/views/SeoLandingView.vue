<template>
  <main class="seo-page" v-if="page" :lang="lang">
    <section class="seo-hero">
      <router-link to="/home" class="seo-brand">gw-link</router-link>
      <p class="seo-kicker">{{ page.kicker }}</p>
      <h1>{{ page.h1 }}</h1>
      <p class="seo-lead">{{ page.lead }}</p>
      <div class="seo-actions">
        <router-link to="/login" class="seo-btn seo-btn--primary">{{ labels.getStarted }}</router-link>
        <router-link to="/home#pricing" class="seo-btn">{{ labels.pricing }}</router-link>
      </div>
    </section>

    <section class="seo-grid" :aria-label="page.kicker">
      <article v-for="item in page.benefits" :key="item.title" class="seo-card">
        <h2>{{ item.title }}</h2>
        <p>{{ item.text }}</p>
      </article>
    </section>

    <section class="seo-section">
      <h2>{{ page.guideTitle }}</h2>
      <ol class="seo-steps">
        <li v-for="step in page.steps" :key="step">{{ step }}</li>
      </ol>
    </section>

    <section v-if="page.snippets?.length" class="seo-section">
      <h2>{{ labels.examples }}</h2>
      <div v-for="snippet in page.snippets" :key="snippet.title + snippet.code" class="seo-snippet">
        <h3 v-if="snippet.title">{{ snippet.title }}</h3>
        <p v-if="snippet.note">{{ snippet.note }}</p>
        <pre><code :class="snippet.lang ? `language-${snippet.lang}` : undefined">{{ snippet.code }}</code></pre>
      </div>
    </section>

    <section class="seo-section seo-faq">
      <h2>{{ labels.faq }}</h2>
      <details v-for="faq in page.faq" :key="faq.q" open>
        <summary>{{ faq.q }}</summary>
        <p>{{ faq.a }}</p>
      </details>
    </section>

    <nav v-if="related.length" class="seo-section" :aria-label="labels.related">
      <h2>{{ labels.related }}</h2>
      <ul class="seo-related">
        <li v-for="item in related" :key="item.path">
          <router-link :to="item.path">{{ item.kicker || item.title }}</router-link>
        </li>
      </ul>
    </nav>

    <footer class="seo-meta">
      <router-link
        v-if="counterpart"
        :to="counterpart.path"
        :hreflang="counterpart.lang || 'zh-CN'"
        :lang="counterpart.lang || 'zh-CN'"
      >{{ labels.switchLang }}</router-link>
      <span v-if="page.updated">{{ labels.updated }}: <time :datetime="page.updated">{{ page.updated }}</time></span>
    </footer>
  </main>
</template>

<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores'
import { setSeoPageTitle } from '@/utils/seoPageTitle'

type Page = {
  path: string
  title: string
  description: string
  kicker: string
  h1: string
  lead: string
  guideTitle: string
  benefits: Array<{ title: string; text: string }>
  steps: string[]
  snippets?: Array<{ title: string; lang: string; code: string; note?: string }>
  faq: Array<{ q: string; a: string }>
  updated?: string
  lang?: string
}

declare global {
  interface Window {
    __SEO_PAGE__?: Page
  }
}

// Fixed UI strings; keep in sync with landingLabels() in backend/internal/web/seo_pages.go
// so the server-rendered HTML and the hydrated SPA say the same thing.
const LABELS = {
  zh: {
    faq: '常见问题',
    related: '相关指南',
    examples: '配置示例',
    updated: '最后更新',
    getStarted: '立即体验',
    pricing: '查看定价',
    switchLang: 'English'
  },
  en: {
    faq: 'FAQ',
    related: 'Related guides',
    examples: 'Configuration examples',
    updated: 'Last updated',
    getStarted: 'Get started',
    pricing: 'View pricing',
    switchLang: '中文版'
  }
} as const

let pagesCache: Promise<Page[]> | null = null
function loadPages(): Promise<Page[]> {
  if (!pagesCache) {
    pagesCache = fetch('/seo/landing-pages.json')
      .then((r) => r.json())
      .catch(() => [])
  }
  return pagesCache
}

const route = useRoute()
const appStore = useAppStore()
const pages = ref<Record<string, Page>>({})
const ordered = ref<Page[]>([])

// Seed from the server-injected page so first paint has content (no fetch flash).
const seeded = window.__SEO_PAGE__
if (seeded?.path) {
  pages.value = { [seeded.path]: seeded }
}
// Then load the full set for client-side navigation and related links.
loadPages().then((arr) => {
  ordered.value = arr
  pages.value = { ...Object.fromEntries(arr.map((p) => [p.path, p])), ...pages.value }
})

const FALLBACK = '/openai-compatible-api-gateway'
const page = computed<Page | undefined>(() => pages.value[route.path] ?? pages.value[FALLBACK])
const lang = computed(() => page.value?.lang || 'zh-CN')
const isEnglish = computed(() => lang.value.toLowerCase().startsWith('en'))
const labels = computed(() => (isEnglish.value ? LABELS.en : LABELS.zh))

const related = computed(() =>
  ordered.value.filter(
    (p) => p.path !== page.value?.path && (p.lang || 'zh-CN').toLowerCase().startsWith('en') === isEnglish.value
  )
)

const counterpart = computed<Page | undefined>(() => {
  const path = page.value?.path
  if (!path) return undefined
  const other = path.startsWith('/en/') ? path.slice(3) : `/en${path}`
  return pages.value[other]
})

// Canonical origin: the admin-configured site URL (so mirror domains point at
// the primary one), else the current origin.
const siteBase = computed(() =>
  (appStore.cachedPublicSettings?.frontend_url || window.location.origin).replace(/\/+$/, '')
)

watchEffect(() => {
  if (!page.value) return
  setSeoPageTitle(route.path, page.value.title)
  document.title = page.value.title
  upsertMeta('description', page.value.description)
  upsertCanonical(`${siteBase.value}${page.value.path}`)
  // Keep <html lang> in sync with the page language so client-side navigation
  // between zh and /en landing pages matches the server-rendered lang.
  document.documentElement.lang = lang.value
})

function upsertMeta(name: string, content: string) {
  let el = document.querySelector<HTMLMetaElement>(`meta[name="${name}"]`)
  if (!el) {
    el = document.createElement('meta')
    el.setAttribute('name', name)
    document.head.appendChild(el)
  }
  el.setAttribute('content', content)
}

function upsertCanonical(href: string) {
  let el = document.querySelector<HTMLLinkElement>('link[rel="canonical"]')
  if (!el) {
    el = document.createElement('link')
    el.setAttribute('rel', 'canonical')
    document.head.appendChild(el)
  }
  el.setAttribute('href', href)
}
</script>

<style scoped>
.seo-page {
  min-height: 100vh;
  background: #0b1020;
  color: #e5ecff;
  padding: 32px 20px 72px;
}
.seo-hero,
.seo-grid,
.seo-section {
  width: min(1080px, 100%);
  margin: 0 auto;
}
.seo-hero {
  padding: 64px 0 40px;
}
.seo-brand {
  color: #8ab4ff;
  font-weight: 800;
  text-decoration: none;
}
.seo-kicker {
  margin-top: 40px;
  color: #7dd3fc;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
h1 {
  max-width: 900px;
  margin: 16px 0;
  font-size: clamp(2.4rem, 6vw, 5rem);
  line-height: 1.05;
}
.seo-lead {
  max-width: 760px;
  color: #b8c3df;
  font-size: 1.2rem;
  line-height: 1.8;
}
.seo-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  margin-top: 28px;
}
.seo-btn {
  display: inline-flex;
  align-items: center;
  border: 1px solid rgba(138, 180, 255, 0.4);
  border-radius: 999px;
  color: #e5ecff;
  padding: 12px 20px;
  text-decoration: none;
}
.seo-btn--primary {
  background: linear-gradient(135deg, #6d5dfc, #14b8a6);
  border-color: transparent;
  color: #fff;
}
.seo-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 18px;
}
.seo-card,
.seo-section {
  border: 1px solid rgba(148, 163, 184, 0.18);
  border-radius: 24px;
  background: rgba(15, 23, 42, 0.72);
  box-shadow: 0 20px 80px rgba(0, 0, 0, 0.25);
}
.seo-card {
  padding: 24px;
}
.seo-card h2,
.seo-section h2 {
  color: #fff;
  margin-top: 0;
}
.seo-card p,
.seo-section p,
.seo-steps {
  color: #b8c3df;
  line-height: 1.75;
}
.seo-section {
  margin-top: 24px;
  padding: 28px;
}
.seo-steps li {
  margin: 10px 0;
}
details {
  border-top: 1px solid rgba(148, 163, 184, 0.18);
  padding: 16px 0;
}
summary {
  cursor: pointer;
  color: #e5ecff;
  font-weight: 700;
}
.seo-snippet + .seo-snippet {
  margin-top: 20px;
}
.seo-snippet h3 {
  color: #e5ecff;
  font-size: 1rem;
  margin: 0 0 8px;
}
.seo-snippet pre {
  margin: 8px 0 0;
  padding: 16px 18px;
  overflow-x: auto;
  border-radius: 14px;
  border: 1px solid rgba(148, 163, 184, 0.18);
  background: #050914;
  color: #dbe7ff;
  font-size: 0.9rem;
  line-height: 1.6;
}
.seo-related {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 10px 24px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.seo-related a,
.seo-meta a {
  color: #8ab4ff;
  text-decoration: none;
}
.seo-related a:hover,
.seo-meta a:hover {
  text-decoration: underline;
}
.seo-meta {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 12px;
  width: min(1080px, 100%);
  margin: 24px auto 0;
  color: #8b97b5;
  font-size: 0.9rem;
}
</style>
