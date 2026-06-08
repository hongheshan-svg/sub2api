#!/usr/bin/env python3
"""SEO/GEO 静态校验。

源真相约定(重要):robots.txt / sitemap.xml / llms.txt 以及 canonical、og:*、
Organization/WebSite/SoftwareApplication 结构化数据,都由后端在响应时动态注入
(backend/internal/web/seo.go + routes/seo.go)。前端 index.html 不应再静态声明
这些标签,否则线上会出现重复 canonical / 重复 JSON-LD。

因此本脚本校验:
1. index.html 含品牌标题、FAQPage JSON-LD、noscript 文案兜底;
2. index.html 不含后端会注入的标签(防止重复回归);
3. public/ 下不存在被后端遮蔽的 robots.txt / sitemap.xml / llms.txt;
4. router 注册了全部 SEO landing 路由,SeoLandingView 含目标关键词。

sitemap/robots/llms 的内容由 backend/internal/web/seo_test.go 校验。
"""
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
INDEX = ROOT / 'index.html'
PUBLIC = ROOT / 'public'

failures = []


def require(cond, msg):
    if not cond:
        failures.append(msg)


html = INDEX.read_text(encoding='utf-8')

# 1. 必须保留的内容
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

# 4. router 注册了全部 SEO landing 路由
router = ROOT / 'src' / 'router' / 'index.ts'
require(router.exists(), 'src/router/index.ts missing')
if router.exists():
    routes = router.read_text(encoding='utf-8')
    require('SeoLandingView.vue' in routes, 'router missing SEO landing component')
    for path in [
        '/claude-code-api-gateway',
        '/claude-code-base-url',
        '/codex-api-gateway',
        '/gemini-cli-api-gateway',
        '/openai-compatible-api-gateway',
        '/gpt-image-2-api',
        '/cc-switch-provider-config',
        '/compare/claude-code-vs-codex',
        '/docs/quick-start',
        '/docs/troubleshooting',
        '/pricing',
    ]:
        require(f"path: '{path}'" in routes or f'path: "{path}"' in routes, f'router missing SEO route {path}')

# 5. SeoLandingView 含目标关键词
landing = ROOT / 'src' / 'views' / 'SeoLandingView.vue'
require(landing.exists(), 'src/views/SeoLandingView.vue missing')
if landing.exists():
    body = landing.read_text(encoding='utf-8')
    for keyword in ['Claude Code API Gateway', 'Codex API Gateway', 'Gemini CLI API Gateway',
                    'OpenAI Compatible API Gateway', 'GPT-Image-2 API']:
        require(keyword in body, f'SeoLandingView missing keyword: {keyword}')

if failures:
    print('SEO validation failed:')
    for f in failures:
        print('-', f)
    sys.exit(1)
print('SEO validation passed')
