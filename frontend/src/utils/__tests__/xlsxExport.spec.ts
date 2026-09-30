import { describe, expect, it } from 'vitest'

// xlsx 来自 frontend/third_party/sheetjs/ 里的 SheetJS 官方 tarball（npm 上的 xlsx 已停更且带
// 高危漏洞）。UsageView 的导出测试 mock 了 xlsx，这里用真实库跑一遍导出用到的 API，
// 保证手动升级 tarball 时不会悄悄破坏导出。
describe('vendored SheetJS xlsx', () => {
  it('is at least 0.20.2, which fixes GHSA-4r6h-8v6p-xvw6 and GHSA-5pgg-2g8v-p4x9', async () => {
    const XLSX = await import('xlsx')
    const [major, minor, patch] = XLSX.version.split('.').map(Number)
    expect(major * 1_000_000 + minor * 1_000 + patch).toBeGreaterThanOrEqual(20_002)
  })

  it('round-trips the usage export workbook', async () => {
    const XLSX = await import('xlsx')
    const ws = XLSX.utils.aoa_to_sheet([['time', 'user', 'cost']])
    XLSX.utils.sheet_add_aoa(ws, [['2026-10-01T00:00:00Z', 'a@example.com', '0.000123']], { origin: -1 })
    XLSX.utils.sheet_add_aoa(ws, [['2026-10-01T00:01:00Z', 'b@example.com', '1.500000']], { origin: -1 })
    const wb = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(wb, ws, 'Usage')

    const data = XLSX.write(wb, { bookType: 'xlsx', type: 'array' })
    expect(data).toBeInstanceOf(ArrayBuffer)

    const parsed = XLSX.read(data, { type: 'array' })
    expect(parsed.SheetNames).toEqual(['Usage'])
    expect(XLSX.utils.sheet_to_json(parsed.Sheets.Usage, { header: 1 })).toEqual([
      ['time', 'user', 'cost'],
      ['2026-10-01T00:00:00Z', 'a@example.com', '0.000123'],
      ['2026-10-01T00:01:00Z', 'b@example.com', '1.500000'],
    ])
  })
})
