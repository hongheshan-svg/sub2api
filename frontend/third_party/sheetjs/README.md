# SheetJS CE (xlsx) — vendored

`xlsx-0.20.3.tgz` 是 SheetJS 官方 CDN 发布的 SheetJS Community Edition（Apache-2.0），
`frontend/package.json` 以 `file:third_party/sheetjs/xlsx-0.20.3.tgz` 引用。

## 为什么不用 npm 上的 `xlsx`

npm 上的 `xlsx` 停在 0.18.5 且不再更新，带两个高危漏洞且 npm 上**没有修复版本**：

- GHSA-4r6h-8v6p-xvw6 / CVE-2023-30533（原型污染，0.19.3 修复）
- GHSA-5pgg-2g8v-p4x9 / CVE-2024-22363（ReDoS，0.20.2 修复）

修复版只从 <https://cdn.sheetjs.com/> 发布。

## 为什么放进仓库而不是直接写 CDN 地址

pnpm 对远程 tarball 依赖不在 lockfile 里记录 integrity，CDN 上的文件被替换也发现不了；
且 CI、发版和 Docker 构建都得能访问 cdn.sheetjs.com。放进仓库后内容随 git 固定，
lockfile 记录 integrity，构建不依赖外网。这也是 SheetJS 官方文档推荐的做法。

## 来源与校验

- 来源：`https://cdn.sheetjs.com/xlsx-0.20.3/xlsx-0.20.3.tgz`（下载于 2026-10-01，与 `xlsx-latest` 别名内容一致）
- SHA-256：`8dc73fc3b00203e72d176e85b50938627c7b086e607c682e8d3c22c02bb99fe8`
- lockfile integrity：`sha512-oLDq3jw7AcLqKWH2AhCpVTZl8mf6X2YReP+Neh0SJUzV/BdZYjth94tG5toiMB1PPrYtxOCfaoUCkvtuH+3AJA==`

## 升级

1. 从 `https://cdn.sheetjs.com/` 下载新版本 `xlsx-<ver>.tgz` 放到本目录，删除旧文件，记录 SHA-256。
2. 在 `frontend/` 用 **pnpm 9**（与 CI 一致）执行 `pnpm add ./third_party/sheetjs/xlsx-<ver>.tgz`，
   提交 `package.json` 与 `pnpm-lock.yaml`。
3. 更新本文件的版本与校验值；`src/utils/__tests__/xlsxExport.spec.ts` 会用真实库验证导出所用 API。

Dockerfile 已在 `pnpm install` 前复制整个 `third_party/` 目录，换版本无需改 Dockerfile。
