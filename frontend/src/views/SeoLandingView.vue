<template>
  <main class="seo-page" v-if="page">
    <section class="seo-hero">
      <router-link to="/home" class="seo-brand">gw-link</router-link>
      <p class="seo-kicker">{{ page.kicker }}</p>
      <h1>{{ page.h1 }}</h1>
      <p class="seo-lead">{{ page.lead }}</p>
      <div class="seo-actions">
        <router-link to="/login" class="seo-btn seo-btn--primary">立即体验</router-link>
        <router-link to="/home#pricing" class="seo-btn">查看定价</router-link>
      </div>
    </section>

    <section class="seo-grid" aria-label="核心价值">
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

    <section class="seo-section seo-faq">
      <h2>常见问题</h2>
      <details v-for="faq in page.faq" :key="faq.q" open>
        <summary>{{ faq.q }}</summary>
        <p>{{ faq.a }}</p>
      </details>
    </section>
  </main>
</template>

<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { useRoute } from 'vue-router'

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
  faq: Array<{ q: string; a: string }>
}

declare global {
  interface Window {
    __SEO_PAGE__?: Page
  }
}

let pagesCache: Promise<Record<string, Page>> | null = null
function loadPages(): Promise<Record<string, Page>> {
  if (!pagesCache) {
    pagesCache = fetch('/seo/landing-pages.json')
      .then((r) => r.json())
      .then((arr: Page[]) => Object.fromEntries(arr.map((p) => [p.path, p])))
      .catch(() => ({}))
  }
  return pagesCache
}

const route = useRoute()
const pages = ref<Record<string, Page>>({})

// Seed from the server-injected page so first paint has content (no fetch flash).
const seeded = window.__SEO_PAGE__
if (seeded?.path) {
  pages.value = { [seeded.path]: seeded }
}
// Then load the full set for client-side navigation to other landing routes.
loadPages().then((p) => {
  pages.value = { ...p, ...pages.value }
})

const FALLBACK = '/openai-compatible-api-gateway'
const page = computed<Page | undefined>(() => pages.value[route.path] ?? pages.value[FALLBACK])

watchEffect(() => {
  if (!page.value) return
  document.title = page.value.title
  upsertMeta('description', page.value.description)
  upsertCanonical(`https://gw-link.com${route.path}`)
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
</style>
