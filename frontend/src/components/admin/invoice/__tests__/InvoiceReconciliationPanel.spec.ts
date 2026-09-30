import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import InvoiceReconciliationPanel from '../InvoiceReconciliationPanel.vue'
import type { InvoiceReconciliationRow } from '@/types/invoice'

const { listReconciliation, getReconciliationSummary, getUserReconciliation, showError } = vi.hoisted(() => ({
  listReconciliation: vi.fn(),
  getReconciliationSummary: vi.fn(),
  getUserReconciliation: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/invoices', () => ({
  adminInvoiceAPI: { listReconciliation, getReconciliationSummary, getUserReconciliation },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() }),
}))

vi.mock('@/utils/format', () => ({
  formatCurrency: (amount: number) => `¥${amount.toFixed(2)}`,
  formatDateTime: (value: string) => value,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}${JSON.stringify(params)}` : key),
    te: () => true,
  }),
}))

const row = (overrides: Partial<InvoiceReconciliationRow>): InvoiceReconciliationRow => ({
  user_id: 1,
  username: 'user',
  email: 'user@example.com',
  paid_amount: 0,
  refunded_amount: 0,
  net_paid_amount: 0,
  invoiced_amount: 0,
  pending_amount: 0,
  uninvoiced_amount: 0,
  over_invoiced_amount: 0,
  duplicate_orders: 0,
  refunded_invoiced_orders: 0,
  mismatched: false,
  ...overrides,
})

const overRow = row({
  user_id: 7,
  username: 'alice',
  paid_amount: 240,
  refunded_amount: 40,
  net_paid_amount: 200,
  invoiced_amount: 140,
  pending_amount: 90,
  over_invoiced_amount: 30,
  duplicate_orders: 1,
  refunded_invoiced_orders: 1,
  mismatched: true,
})
const balancedRow = row({ user_id: 8, username: 'bob', paid_amount: 80, net_paid_amount: 80, invoiced_amount: 80 })
const uninvoicedRow = row({ user_id: 9, username: 'carol', paid_amount: 100, net_paid_amount: 100, invoiced_amount: 80, uninvoiced_amount: 20 })

const wrappers: ReturnType<typeof mount>[] = []

function mountPanel() {
  const wrapper = mount(InvoiceReconciliationPanel, {
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show" data-test="detail-dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
        },
        Pagination: true,
        Select: true,
        Icon: true,
      },
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

describe('InvoiceReconciliationPanel', () => {
  beforeEach(() => {
    listReconciliation.mockReset().mockResolvedValue({
      data: { items: [overRow, balancedRow, uninvoicedRow], total: 3, page: 1, page_size: 20 },
    })
    getReconciliationSummary.mockReset().mockResolvedValue({
      data: {
        paid_amount: 420,
        refunded_amount: 40,
        net_paid_amount: 380,
        invoiced_amount: 300,
        pending_amount: 90,
        uninvoiced_amount: 20,
        over_invoiced_amount: 30,
        user_count: 3,
        mismatched_users: 1,
      },
    })
    getUserReconciliation.mockReset()
    showError.mockReset()
  })

  afterEach(() => {
    wrappers.splice(0).forEach((w) => w.unmount())
  })

  it('loads summary and rows with the invoiced scope by default and highlights differences', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    expect(getReconciliationSummary).toHaveBeenCalledTimes(1)
    expect(listReconciliation).toHaveBeenCalledWith({ page: 1, page_size: 20, scope: 'invoiced' })

    const over = wrapper.get('[data-test="summary-over"]')
    expect(over.text()).toBe('¥30.00')
    expect(over.classes()).toContain('text-red-600')

    const rows = wrapper.findAll('[data-test="reconciliation-row"]')
    expect(rows).toHaveLength(3)
    expect(rows[0].classes()).toContain('bg-red-50/60')
    expect(rows[1].classes()).not.toContain('bg-red-50/60')

    const diffs = wrapper.findAll('[data-test="reconciliation-diff"]').map((d) => d.text())
    expect(diffs[0]).toBe('invoice.reconciliation.diffOver{"amount":"¥30.00"}')
    expect(diffs[1]).toBe('invoice.reconciliation.diffBalanced')
    expect(diffs[2]).toBe('invoice.reconciliation.diffUninvoiced{"amount":"¥20.00"}')

    expect(rows[0].text()).toContain('invoice.reconciliation.issues.duplicate{"count":1}')
    expect(rows[0].text()).toContain('invoice.reconciliation.issues.refunded_invoiced{"count":1}')
    expect(rows[0].text()).toContain('invoice.reconciliation.paidBreakdown')
  })

  it('searches by keyword from the first page', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    listReconciliation.mockClear()

    await wrapper.get('input[type="text"]').setValue('  alice@example.com ')
    await wrapper.get('input[type="text"]').trigger('keyup.enter')
    await flushPromises()

    expect(listReconciliation).toHaveBeenCalledWith({ page: 1, page_size: 20, scope: 'invoiced', keyword: 'alice@example.com' })
  })

  it('opens per-order detail with issues and uninvoiced orders', async () => {
    getUserReconciliation.mockResolvedValue({
      data: {
        summary: overRow,
        orders: [
          {
            id: 11,
            out_trade_no: 'sub2_dup',
            pay_amount: 20,
            refunded_amount: 0,
            status: 'completed',
            order_type: 'balance',
            payment_type: 'stripe',
            created_at: '2026-09-30T00:00:00Z',
            requests: [
              { id: 1, serial_no: 'INV-A', status: 'pending' },
              { id: 2, serial_no: 'INV-B', status: 'pending' },
            ],
            issues: ['duplicate'],
          },
          {
            id: 12,
            out_trade_no: 'sub2_free',
            pay_amount: 30,
            refunded_amount: 0,
            status: 'completed',
            order_type: 'balance',
            payment_type: 'stripe',
            created_at: '2026-09-30T00:00:00Z',
            requests: [],
            issues: [],
          },
        ],
      },
    })
    const wrapper = mountPanel()
    await flushPromises()

    await wrapper.findAll('[data-test="reconciliation-row"]')[0].get('button').trigger('click')
    await flushPromises()

    expect(getUserReconciliation).toHaveBeenCalledWith(7)
    const orders = wrapper.findAll('[data-test="reconciliation-order"]')
    expect(orders).toHaveLength(2)
    expect(orders[0].classes()).toContain('bg-red-50/60')
    expect(orders[0].text()).toContain('INV-A')
    expect(orders[0].text()).toContain('INV-B')
    expect(orders[0].text()).toContain('invoice.reconciliation.orderIssues.duplicate')
    expect(orders[1].text()).toContain('invoice.reconciliation.notInvoiced')
  })

  it('closes the detail dialog and reports the error when loading fails', async () => {
    getUserReconciliation.mockRejectedValue(new Error('boom'))
    const wrapper = mountPanel()
    await flushPromises()

    await wrapper.findAll('[data-test="reconciliation-row"]')[0].get('button').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalled()
    expect(wrapper.find('[data-test="detail-dialog"]').exists()).toBe(false)
  })
})
