# 精简版 SEO / GEO 优化 设计文档

日期:2026-05-29
目标:让更多人(搜索引擎 + 生成式 AI 引擎)发现并正确理解本系统。在当前 `main` 上新增轻量 SEO/GEO 基础设施,不引入 SSR 框架、不新增页面。

## 1. 背景

当前 `main` **完全没有 SEO/GEO 基础设施**:

- `frontend/index.html` 是裸 SPA 外壳:只有 `charset` / `viewport` / `<title>` / favicon,**无 description、无 Open Graph / Twitter、无 canonical、无结构化数据**。
- 没有 `/robots.txt`、`/sitemap.xml`、`/llms.txt`。
- 客户端渲染(`<div id="app">`),不执行 JS 的爬虫/社交抓取/部分 AI 爬虫只能看到空壳。

> 仓库另有 `origin/feat/seo-geo` 分支(25 commits,含 SSR HeadInjector、双语 FAQ、schema 校验),但落后 main 381 个提交且未合并。本方案**参考其思路,在 main 上重做精简版**,不合并该分支。

产品为**自部署**,每个部署域名不同 → 绝对 URL(canonical / og:url / sitemap / llms)必须来自**配置的站点 URL**,不能硬编码。

## 2. 范围

**做**:技术 SEO(index.html meta + 结构化数据)+ GEO 文件(robots/sitemap/llms,放行 AI 爬虫)。
**不做**:新增页面(如 FAQ)、per-route 动态 meta、双语 hreflang、SSR 框架、新增管理员设置项。

## 3. 架构 —— 复用既有 HTML 注入接缝

生产环境由 Go `web.FrontendServer` 提供内嵌 `dist/index.html`(`backend/internal/web/embed_on.go`):

- `serveIndexHTML()`(约 142-191)按设置渲染并缓存 HTML;`HTMLCache` 以 baseHTML hash + 设置 JSON hash 为键,设置变更时经 `router.go` 回调 `InvalidateCache()` 失效。
- `injectSettings()`(约 193-206)在 `</head>` 前注入 `window.__APP_CONFIG__` 脚本,并由 `injectSiteTitle()`(约 210-231)用站点名替换 `<title>`。

**本方案在 `injectSettings()` 同一接缝再注入一段 SEO head**。因为 SEO head 仅依赖站点设置,设置变更即缓存失效 → 注入内容与缓存天然一致;且不依赖每请求 Host(用配置 URL),缓存仍可命中。

SPA 为客户端渲染,catch-all 对所有前端路由返回同一外壳 → **本期 meta 为全站统一**(首页级),不做 per-route。

## 4. 组件

### 4.1 `frontend/index.html` 静态基线(语言/部署无关的默认值)

在 `<head>` 内补齐(放在 `<title>` 之后):

```html
<meta name="description" content="统一接入 Claude、GPT、Gemini 等大模型 API,兼容主流 IDE 插件与 CLI 工具,只需替换 base_url、30 秒完成接入的企业级 AI 编码中转网关。" />
<meta name="keywords" content="AI API, Claude, GPT, Gemini, API 网关, 中转, ..." />
<meta name="robots" content="index, follow" />
<meta name="theme-color" content="#1a1a2e" />
<meta property="og:type" content="website" />
<meta property="og:image" content="/logo.png" />
<meta name="twitter:card" content="summary_large_image" />
```

作用:即使在 `embed_off`(开发)或未注入时也有基础 meta。生产环境的部署相关绝对 URL 与结构化数据由 4.2 服务端注入补全/覆盖。

### 4.2 服务端动态 SEO head(新增 `backend/internal/web/seo.go`)

新增纯函数 `BuildSEOHead(in SEOInput) []byte`,返回要插入 `</head>` 前的 SEO 片段:

输入:
```go
type SEOInput struct {
    SiteName    string // 站点名(GetSiteName,默认 Sub2API)
    SiteSubtitle string // 副标题(GetPublicSettings 已有,默认 "Subscription to API Conversion Platform")
    BaseURL     string // 站点绝对 URL(GetFrontendURL,可能为空)
    Logo        string // 站点 logo 路径(site_logo,默认 /logo.png)
    Lang        string // "zh-CN"
}
```

输出包含:
- `<link rel="canonical" href="{BaseURL}/">`(BaseURL 为空时省略)
- `og:url`(= BaseURL,空则省略)、`og:site_name`、`og:title`、`og:description`、绝对 `og:image`(BaseURL+Logo,空则相对)
- `twitter:title` / `twitter:description`
- **JSON-LD**(`<script type="application/ld+json">`),三类:
  - `Organization`(name、url、logo)
  - `WebSite`(name、url)
  - `SoftwareApplication`(name、applicationCategory: `DeveloperApplication`、operatingSystem: `Any`、description)

安全:所有插值经 HTML 属性转义;JSON-LD 内对 `</script>` 转义(`<\/script>`),避免脚本逃逸/XSS。

注入接入点:在 `injectSettings()` 中,从注入用的设置中取 SiteName / SiteSubtitle / BaseURL / Logo,调用 `BuildSEOHead`,在 `</head>` 前插入(与现有 `__APP_CONFIG__` 脚本一并)。`FrontendServer` 通过其持有的 `PublicSettingsProvider` 获取这些值;为拿到 `frontend_url`,在 `PublicSettingsInjectionPayload` 增加 `frontend_url`(无害,站点自身 URL),由 `GetFrontendURL` 填充。

> 标题:保留现有 `injectSiteTitle` 行为(`{SiteName} - AI API Gateway`)。SEO 的 `og:title` 与之一致。

### 4.3 `GET /robots.txt`

动态生成(读 BaseURL):

```
User-agent: *
Allow: /
Disallow: /admin
Disallow: /dashboard
Disallow: /api/
Disallow: /v1/
Disallow: /backend-api/

# 明确放行主流 AI 爬虫(GEO)
User-agent: GPTBot
Allow: /
User-agent: ClaudeBot
Allow: /
User-agent: PerplexityBot
Allow: /
User-agent: Google-Extended
Allow: /
User-agent: CCBot
Allow: /

Sitemap: {BaseURL}/sitemap.xml
```

BaseURL 为空时:Sitemap 行用相对 `/sitemap.xml` 或省略(实现取请求 Host 兜底,见 4.6)。

### 4.4 `GET /sitemap.xml`

列出公开营销路由,绝对 URL:`/`、`/home`、`/login`。

```xml
<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>{BaseURL}/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>
  <url><loc>{BaseURL}/home</loc><changefreq>weekly</changefreq><priority>0.9</priority></url>
  <url><loc>{BaseURL}/login</loc><changefreq>monthly</changefreq><priority>0.5</priority></url>
</urlset>
```

### 4.5 `GET /llms.txt`(GEO 核心)

按 llms.txt 约定输出 markdown:

```
# {SiteName}

> {SiteSubtitle}

{SiteName} 是一个 AI API 网关 / 中转平台,统一接入 Claude、GPT、Gemini 等模型……

## 主要页面
- [首页]({BaseURL}/): 产品介绍与定价
- [登录]({BaseURL}/login): 用户登录/注册
- [文档]({DocURL}): 接入文档   # 仅当 doc_url 配置时

## 说明
- 统一 base_url 与密钥格式,兼容主流 IDE 插件与 CLI 工具。
```

### 4.6 路由接线

- 在 `shouldBypassEmbeddedFrontend()`(`embed_on.go` 约 291-303)增加放行:`/robots.txt`、`/sitemap.xml`、`/llms.txt`(精确匹配),否则被 SPA catch-all 吞掉。
- 在 `router.go`(`settingService` 可用处)注册三个 GET handler。handler 取 BaseURL:优先 `GetFrontendURL`;为空则用请求 `scheme://Host`(`X-Forwarded-Proto` + `Host`)兜底。
- 三个端点内容很小,可每请求计算;`robots`/`llms`/`sitemap` 设 `Cache-Control: public, max-age=3600` 与正确 `Content-Type`(`text/plain; charset=utf-8`、`application/xml`、`text/plain; charset=utf-8`)。

## 5. 数据来源(复用,无新增设置)

| 用途 | 来源 | 默认 |
|---|---|---|
| 站点名 | `GetSiteName` / `site_name` | `Sub2API` |
| 副标题/描述 | `site_subtitle`(GetPublicSettings) | `Subscription to API Conversion Platform` |
| 站点绝对 URL | `GetFrontendURL` / `frontend_url`(配置兜底) | 空→请求 Host 兜底 |
| Logo | `site_logo` | `/logo.png` |
| 文档链接 | `doc_url` | 空→llms 省略该行 |

## 6. 测试

后端单元测试(`//go:build unit`,新增 `backend/internal/web/seo_test.go`):
- `BuildSEOHead`:含 canonical/og/JSON-LD;BaseURL 为空时省略绝对 URL 标签;`</script>` 被转义;站点名出现在 og:title。
- `robots.txt`:含各 AI 爬虫 User-agent + Sitemap 行;BaseURL 兜底逻辑。
- `sitemap.xml`:合法 XML、含三条 loc、绝对 URL。
- `llms.txt`:含站点名标题、副标题、链接;doc_url 为空时不输出文档行。
- 路由:`shouldBypassEmbeddedFrontend` 对三个路径返回 true;catch-all 不再吞掉。

手动验证:`curl /robots.txt /sitemap.xml /llms.txt`;查看首页 HTML `<head>` 含 meta/OG/JSON-LD;用 Rich Results / OG 调试器抽查。

## 7. 涉及文件

- `frontend/index.html`(改)— 静态基线 meta
- `backend/internal/web/seo.go`(新)— `BuildSEOHead` + robots/sitemap/llms 生成函数
- `backend/internal/web/seo_test.go`(新,`//go:build unit`)
- `backend/internal/web/embed_on.go`(改)— `injectSettings` 注入 SEO head;`shouldBypassEmbeddedFrontend` 放行三路径
- `backend/internal/service/setting_service.go`(改)— `PublicSettingsInjectionPayload` 增 `frontend_url` + 注入填充
- `backend/internal/handler/dto/settings.go`(改)— dto.PublicSettings 增 `frontend_url`(保持 schema-drift 测试通过)
- `backend/internal/server/router.go`(改)— 注册三个 GET handler

## 8. 兼容性与风险

- 注入仅在 `embed_on`(生产)生效;`embed_off`(开发)只有 4.1 的静态基线 meta —— 可接受。
- `frontend_url` 加入公共设置注入:需同时改 `dto.PublicSettings` 与 `PublicSettingsInjectionPayload`(否则 `TestPublicSettingsInjectionPayload_SchemaDoesNotDrift` 失败)。
- 全站统一 meta:本期不做 per-route;后续若需要可引入客户端 `useSeoHead`。
- 缓存:SEO head 进入 HTMLCache,随设置变更失效;不依赖每请求 Host,缓存命中不受影响。
