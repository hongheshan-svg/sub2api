#!/usr/bin/env python3
"""SEO/GEO 静态校验。

源真相约定(重要):
- robots.txt / sitemap.xml / llms.txt 以及 canonical、og:*、Organization/WebSite/
  SoftwareApplication 结构化数据,都由后端在响应时动态注入
  (backend/internal/web/seo.go + routes/seo.go)。前端 index.html 不应再静态声明
  这些标签,否则线上会出现重复 canonical / 重复 JSON-LD。
- 每个 SEO 落地页的内容只有一个来源:public/seo/landing-pages.json。后端按路由
  注入 per-page head + 正文,前端 SeoLandingView 也消费它。

因此本脚本校验:
1. index.html 含品牌标题、FAQPage JSON-LD、noscript 文案兜底;
2. index.html 不含后端会注入的标签(防止重复回归);
3. public/ 下不存在被后端遮蔽的 robots.txt / sitemap.xml / llms.txt;
4. public/seo/landing-pages.json 存在、可解析、字段完整,并覆盖目标关键词;
5. landing-pages.json 中每个 path 都在 router 注册,SeoLandingView 消费该 JSON。

sitemap/robots/llms 以及 per-page head/正文的内容由 backend/internal/web 的
Go 测试(seo_test.go / embed_test.go)校验。
"""
from pathlib import Path
import json
import sys

ROOT = Path(__file__).resolve().parents[1]
INDEX = ROOT / 'index.html'
PUBLIC = ROOT / 'public'

failures = []


def require(cond, msg):
    if not cond:
        failures.append(msg)


html = INDEX.read_text(encoding='utf-8')

# 1. index.html 必须保留的内容
require('gw-link - AI API Gateway' in html, 'index.html title should use gw-link brand')
require('application/ld+json' in html, 'index.html missing JSON-LD structured data')
require('"@type": "FAQPage"' in html, 'index.html missing FAQPage JSON-LD (backend does not inject this)')
require('AI API Gateway for Claude Code, Codex and Gemini CLI' in html,
        'index.html missing static SEO H1 fallback text (noscript)')

# 2. 后端注入的标签不应在 index.html 静态出现(避免重复)
for tag, msg in [
    ('<link rel="canonical"', 'index.html must NOT hardcode canonical (backend injects it -> duplicate)'),
    ('property="og:title"', 'index.html must NOT hardcode og:title (backend injects it -> duplicate)'),
    ('property="og:url"', 'index.html must NOT hardcode og:url (backend injects it -> duplicate)'),
    ('"@type": "Organization"', 'index.html must NOT hardcode Organization JSON-LD (backend injects it -> duplicate)'),
    ('"@type": "SoftwareApplication"', 'index.html must NOT hardcode SoftwareApplication JSON-LD (backend injects it -> duplicate)'),
]:
    require(tag not in html, msg)

# 3. public 下不应存在被后端动态路由遮蔽的静态文件
for name in ('robots.txt', 'sitemap.xml', 'llms.txt'):
    require(not (PUBLIC / name).exists(),
            f'public/{name} is shadowed by the backend dynamic route — remove it (edit backend/internal/web/seo.go instead)')

# 4. landing-pages.json 是落地页内容的唯一来源
PAGES_JSON = PUBLIC / 'seo' / 'landing-pages.json'
require(PAGES_JSON.exists(), 'public/seo/landing-pages.json missing (single content source)')
data = []
json_paths = []
if PAGES_JSON.exists():
    try:
        data = json.loads(PAGES_JSON.read_text(encoding='utf-8'))
    except Exception as e:  # noqa: BLE001
        failures.append(f'landing-pages.json does not parse: {e}')
        data = []
    require(len(data) >= 1, 'landing-pages.json should contain at least one page')
    for p in data:
        path = p.get('path', '?')
        json_paths.append(p.get('path', ''))
        for field in ('path', 'title', 'description', 'h1', 'kicker'):
            require(bool(p.get(field)), f'landing-pages.json entry {path} missing {field}')
        require(isinstance(p.get('faq'), list) and len(p.get('faq') or []) >= 1,
                f'landing-pages.json entry {path} needs >=1 faq')
    # 目标关键词应被落地页内容覆盖(关键词现在存于 JSON,不再存于 .vue)
    blob = json.dumps(data, ensure_ascii=False)
    for keyword in ['Claude Code API Gateway', 'Codex API Gateway', 'Gemini CLI API Gateway',
                    'OpenAI Compatible API Gateway', 'GPT-Image-2 API']:
        require(keyword in blob, f'landing-pages.json missing target keyword: {keyword}')
    # 4b. 中英配对完整性 + lang 字段（hreflang 需要 reciprocal pairs：每个 /en/x 必须有 /x，反之亦然）
    pset = set(json_paths)
    for p in data:
        path = p.get('path', '?')
        expected_lang = 'en' if path.startswith('/en/') else 'zh-CN'
        require(p.get('lang') == expected_lang,
                f'landing-pages.json entry {path} lang should be "{expected_lang}" (got {p.get("lang")!r})')
        counterpart = path[3:] if path.startswith('/en/') else '/en' + path
        require(counterpart in pset,
                f'landing-pages.json entry {path} missing hreflang counterpart {counterpart}')

# 5. router 注册了 landing-pages.json 中的每个 path
router = ROOT / 'src' / 'router' / 'index.ts'
require(router.exists(), 'src/router/index.ts missing')
if router.exists():
    routes = router.read_text(encoding='utf-8')
    require('SeoLandingView.vue' in routes, 'router missing SEO landing component')
    for path in json_paths:
        require(f"path: '{path}'" in routes or f'path: "{path}"' in routes,
                f'router missing SEO route {path} (declared in landing-pages.json)')

# 6. SeoLandingView 消费共享 JSON(内容不再内联在组件里)
landing = ROOT / 'src' / 'views' / 'SeoLandingView.vue'
require(landing.exists(), 'src/views/SeoLandingView.vue missing')
if landing.exists():
    body = landing.read_text(encoding='utf-8')
    require('/seo/landing-pages.json' in body,
            'SeoLandingView must load /seo/landing-pages.json (single content source)')

if failures:
    print('SEO validation failed:')
    for f in failures:
        print('-', f)
    sys.exit(1)
print('SEO validation passed')
