<template>
  <div class="space-y-4">
    <div class="card p-4 text-sm text-gray-600 dark:text-gray-300">
      {{ t('invoice.reconciliation.hint') }}
    </div>

    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
      <div class="card p-4">
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.netPaid') }}</div>
        <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white" data-test="summary-net-paid">
          {{ formatCurrency(summary?.net_paid_amount ?? 0) }}
        </div>
        <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('invoice.reconciliation.cards.netPaidSub', {
            paid: formatCurrency(summary?.paid_amount ?? 0),
            refunded: formatCurrency(summary?.refunded_amount ?? 0),
          }) }}
        </div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.invoiced') }}</div>
        <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">
          {{ formatCurrency(summary?.invoiced_amount ?? 0) }}
        </div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.pending') }}</div>
        <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">
          {{ formatCurrency(summary?.pending_amount ?? 0) }}
        </div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.uninvoiced') }}</div>
        <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">
          {{ formatCurrency(summary?.uninvoiced_amount ?? 0) }}
        </div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.overInvoiced') }}</div>
        <div
          :class="[
            'mt-1 text-lg font-semibold',
            (summary?.over_invoiced_amount ?? 0) > 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-900 dark:text-white'
          ]"
          data-test="summary-over"
        >
          {{ formatCurrency(summary?.over_invoiced_amount ?? 0) }}
        </div>
        <div
          v-if="(summary?.mismatched_users ?? 0) > 0"
          class="mt-1 text-xs font-medium text-red-600 dark:text-red-400"
        >
          {{ t('invoice.reconciliation.cards.mismatchedUsers', { count: summary?.mismatched_users ?? 0 }) }}
        </div>
      </div>
    </div>

    <div class="card p-4">
      <div class="grid gap-3 md:grid-cols-[200px_1fr_auto] md:items-end">
        <div>
          <label class="input-label">{{ t('invoice.reconciliation.scopeLabel') }}</label>
          <Select v-model="filters.scope" :options="scopeOptions" class="mt-1" @change="search" />
        </div>
        <div>
          <label class="input-label">{{ t('invoice.reconciliation.keywordPlaceholder') }}</label>
          <input
            v-model="filters.keyword"
            type="text"
            class="input mt-1 w-full"
            :placeholder="t('invoice.reconciliation.keywordPlaceholder')"
            @keyup.enter="search"
          />
        </div>
        <div class="flex gap-2">
          <button type="button" class="btn btn-primary" @click="search">{{ t('common.search') }}</button>
          <button type="button" class="btn btn-secondary" @click="resetFilters">{{ t('common.reset') }}</button>
        </div>
      </div>
    </div>

    <div class="card overflow-hidden">
      <div v-if="loading" class="flex items-center justify-center py-16">
        <Icon name="refresh" size="lg" class="animate-spin text-gray-400" />
      </div>
      <div
        v-else-if="rows.length === 0"
        class="flex flex-col items-center justify-center gap-3 py-16 text-gray-500 dark:text-gray-400"
      >
        <Icon name="chartBar" size="xl" />
        <p class="text-sm">{{ t('invoice.reconciliation.empty') }}</p>
      </div>
      <div v-else class="overflow-x-auto">
        <table class="min-w-full text-sm">
          <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
            <tr>
              <th class="px-4 py-3 font-medium">{{ t('invoice.reconciliation.columns.user') }}</th>
              <th class="px-4 py-3 text-right font-medium">{{ t('invoice.reconciliation.columns.netPaid') }}</th>
              <th class="px-4 py-3 text-right font-medium">{{ t('invoice.reconciliation.columns.invoiced') }}</th>
              <th class="px-4 py-3 text-right font-medium">{{ t('invoice.reconciliation.columns.pending') }}</th>
              <th class="px-4 py-3 text-right font-medium">{{ t('invoice.reconciliation.columns.diff') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('invoice.reconciliation.columns.issues') }}</th>
              <th class="px-4 py-3"></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr
              v-for="row in rows"
              :key="row.user_id"
              :class="row.mismatched ? 'bg-red-50/60 dark:bg-red-900/10' : ''"
              data-test="reconciliation-row"
            >
              <td class="px-4 py-3">
                <div class="font-medium text-gray-900 dark:text-white">{{ row.username || '-' }}</div>
                <div class="text-xs text-gray-500 dark:text-gray-400">#{{ row.user_id }} · {{ row.email }}</div>
              </td>
              <td class="px-4 py-3 text-right">
                <div class="font-medium text-gray-900 dark:text-white">{{ formatCurrency(row.net_paid_amount) }}</div>
                <div v-if="row.refunded_amount > 0" class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('invoice.reconciliation.paidBreakdown', {
                    paid: formatCurrency(row.paid_amount),
                    refunded: formatCurrency(row.refunded_amount),
                  }) }}
                </div>
              </td>
              <td class="px-4 py-3 text-right text-gray-700 dark:text-gray-200">{{ formatCurrency(row.invoiced_amount) }}</td>
              <td class="px-4 py-3 text-right text-gray-700 dark:text-gray-200">{{ formatCurrency(row.pending_amount) }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right" data-test="reconciliation-diff">
                <span v-if="row.over_invoiced_amount > 0" class="font-semibold text-red-600 dark:text-red-400">
                  {{ t('invoice.reconciliation.diffOver', { amount: formatCurrency(row.over_invoiced_amount) }) }}
                </span>
                <span v-else-if="row.uninvoiced_amount > 0" class="text-gray-600 dark:text-gray-300">
                  {{ t('invoice.reconciliation.diffUninvoiced', { amount: formatCurrency(row.uninvoiced_amount) }) }}
                </span>
                <span v-else class="text-emerald-600 dark:text-emerald-400">
                  {{ t('invoice.reconciliation.diffBalanced') }}
                </span>
              </td>
              <td class="px-4 py-3">
                <div class="flex flex-wrap gap-1">
                  <span
                    v-if="row.duplicate_orders > 0"
                    :class="issueBadgeClass('duplicate')"
                    :title="t('invoice.reconciliation.issueHints.duplicate')"
                  >
                    {{ t('invoice.reconciliation.issues.duplicate', { count: row.duplicate_orders }) }}
                  </span>
                  <span
                    v-if="row.refunded_invoiced_orders > 0"
                    :class="issueBadgeClass('refunded_invoiced')"
                    :title="t('invoice.reconciliation.issueHints.refunded_invoiced')"
                  >
                    {{ t('invoice.reconciliation.issues.refunded_invoiced', { count: row.refunded_invoiced_orders }) }}
                  </span>
                  <span
                    v-if="row.duplicate_orders === 0 && row.refunded_invoiced_orders === 0"
                    class="text-gray-400"
                  >-</span>
                </div>
              </td>
              <td class="px-4 py-3 text-right">
                <button type="button" class="btn btn-secondary btn-sm" @click="openDetail(row)">
                  {{ t('invoice.reconciliation.viewDetail') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <Pagination
        v-if="pagination.total > 0"
        :page="pagination.page"
        :total="pagination.total"
        :page-size="pagination.page_size"
        @update:page="handlePageChange"
        @update:pageSize="handlePageSizeChange"
      />
    </div>

    <BaseDialog
      :show="detailOpen"
      :title="t('invoice.reconciliation.detailTitle', { user: detailUser?.username || detailUser?.email || `#${detailUser?.user_id ?? ''}` })"
      width="extra-wide"
      @close="closeDetail"
    >
      <div v-if="detailLoading" class="flex items-center justify-center py-12">
        <Icon name="refresh" size="lg" class="animate-spin text-gray-400" />
      </div>
      <div v-else-if="detail" class="space-y-4">
        <div class="grid gap-3 text-sm sm:grid-cols-5">
          <div>
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.netPaid') }}</div>
            <div class="font-semibold text-gray-900 dark:text-white">{{ formatCurrency(detail.summary.net_paid_amount) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.invoiced') }}</div>
            <div class="font-semibold text-gray-900 dark:text-white">{{ formatCurrency(detail.summary.invoiced_amount) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.pending') }}</div>
            <div class="font-semibold text-gray-900 dark:text-white">{{ formatCurrency(detail.summary.pending_amount) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.uninvoiced') }}</div>
            <div class="font-semibold text-gray-900 dark:text-white">{{ formatCurrency(detail.summary.uninvoiced_amount) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('invoice.reconciliation.cards.overInvoiced') }}</div>
            <div
              :class="[
                'font-semibold',
                detail.summary.over_invoiced_amount > 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-900 dark:text-white'
              ]"
            >
              {{ formatCurrency(detail.summary.over_invoiced_amount) }}
            </div>
          </div>
        </div>

        <div v-if="detail.orders.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('invoice.reconciliation.noOrders') }}
        </div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full text-sm">
            <thead class="text-left text-xs text-gray-500 dark:text-gray-400">
              <tr>
                <th class="px-2 py-2 font-medium">{{ t('invoice.reconciliation.orderColumns.orderNo') }}</th>
                <th class="px-2 py-2 text-right font-medium">{{ t('invoice.reconciliation.orderColumns.payAmount') }}</th>
                <th class="px-2 py-2 text-right font-medium">{{ t('invoice.reconciliation.orderColumns.refunded') }}</th>
                <th class="px-2 py-2 font-medium">{{ t('invoice.reconciliation.orderColumns.status') }}</th>
                <th class="px-2 py-2 font-medium">{{ t('invoice.reconciliation.orderColumns.requests') }}</th>
                <th class="px-2 py-2 font-medium">{{ t('invoice.reconciliation.orderColumns.issues') }}</th>
                <th class="px-2 py-2 font-medium">{{ t('invoice.reconciliation.orderColumns.createdAt') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr
                v-for="order in detail.orders"
                :key="order.id"
                :class="order.issues.length ? 'bg-red-50/60 dark:bg-red-900/10' : ''"
                data-test="reconciliation-order"
              >
                <td class="px-2 py-2 font-mono text-xs text-gray-700 dark:text-gray-200">{{ order.out_trade_no }}</td>
                <td class="px-2 py-2 text-right text-gray-900 dark:text-white">{{ formatCurrency(order.pay_amount) }}</td>
                <td class="px-2 py-2 text-right text-gray-600 dark:text-gray-300">
                  {{ order.refunded_amount > 0 ? formatCurrency(order.refunded_amount) : '-' }}
                </td>
                <td class="whitespace-nowrap px-2 py-2 text-gray-600 dark:text-gray-300">{{ orderStatusLabel(order.status) }}</td>
                <td class="px-2 py-2">
                  <div v-if="order.requests.length" class="flex flex-col gap-1">
                    <div v-for="req in order.requests" :key="req.id" class="flex flex-wrap items-center gap-1">
                      <span class="font-mono text-xs text-gray-700 dark:text-gray-200">{{ req.serial_no }}</span>
                      <span :class="requestStatusClass(req.status)">{{ t(`invoice.status.${req.status}`) }}</span>
                      <span v-if="req.invoice_no" class="font-mono text-xs text-gray-500 dark:text-gray-400">{{ req.invoice_no }}</span>
                    </div>
                  </div>
                  <span v-else class="text-xs text-gray-400">{{ t('invoice.reconciliation.notInvoiced') }}</span>
                </td>
                <td class="px-2 py-2">
                  <div class="flex flex-wrap gap-1">
                    <span
                      v-for="issue in order.issues"
                      :key="issue"
                      :class="issueBadgeClass(issue)"
                      :title="t(`invoice.reconciliation.issueHints.${issue}`)"
                    >
                      {{ t(`invoice.reconciliation.orderIssues.${issue}`) }}
                    </span>
                    <span v-if="order.issues.length === 0" class="text-gray-400">-</span>
                  </div>
                </td>
                <td class="whitespace-nowrap px-2 py-2 text-xs text-gray-500 dark:text-gray-400">{{ formatDateTime(order.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end">
          <button type="button" class="btn btn-secondary" @click="closeDetail">{{ t('common.close') }}</button>
        </div>
      </template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminInvoiceAPI } from '@/api/admin/invoices'
import { useAppStore } from '@/stores'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatCurrency, formatDateTime } from '@/utils/format'
import type { SelectOption } from '@/types'
import type {
  InvoiceReconciliationDetail,
  InvoiceReconciliationIssue,
  InvoiceReconciliationListParams,
  InvoiceReconciliationRow,
  InvoiceReconciliationScope,
  InvoiceReconciliationSummary,
  InvoiceStatus
} from '@/types/invoice'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const DEFAULT_SCOPE: InvoiceReconciliationScope = 'invoiced'

const { t, te } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const rows = ref<InvoiceReconciliationRow[]>([])
const summary = ref<InvoiceReconciliationSummary | null>(null)
const pagination = reactive({ page: 1, page_size: 20, total: 0 })
const filters = reactive<{ scope: InvoiceReconciliationScope; keyword: string }>({
  scope: DEFAULT_SCOPE,
  keyword: ''
})

const detailOpen = ref(false)
const detailLoading = ref(false)
const detailUser = ref<InvoiceReconciliationRow | null>(null)
const detail = ref<InvoiceReconciliationDetail | null>(null)

const scopeOptions = computed<SelectOption[]>(() => [
  { value: 'invoiced', label: t('invoice.reconciliation.scopes.invoiced') },
  { value: 'mismatched', label: t('invoice.reconciliation.scopes.mismatched') },
  { value: 'all', label: t('invoice.reconciliation.scopes.all') }
])

async function fetchSummary() {
  try {
    const res = await adminInvoiceAPI.getReconciliationSummary()
    summary.value = res.data
  } catch (err: unknown) {
    showError(err)
  }
}

async function fetchList() {
  loading.value = true
  try {
    const params: InvoiceReconciliationListParams = {
      page: pagination.page,
      page_size: pagination.page_size,
      scope: filters.scope
    }
    const keyword = filters.keyword.trim()
    if (keyword) params.keyword = keyword
    const res = await adminInvoiceAPI.listReconciliation(params)
    rows.value = res.data.items || []
    pagination.total = res.data.total || 0
  } catch (err: unknown) {
    showError(err)
  } finally {
    loading.value = false
  }
}

function search() {
  pagination.page = 1
  void fetchSummary()
  void fetchList()
}

function resetFilters() {
  filters.scope = DEFAULT_SCOPE
  filters.keyword = ''
  search()
}

function handlePageChange(page: number) {
  pagination.page = page
  void fetchList()
}

function handlePageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  void fetchList()
}

async function openDetail(row: InvoiceReconciliationRow) {
  detailUser.value = row
  detail.value = null
  detailOpen.value = true
  detailLoading.value = true
  try {
    const res = await adminInvoiceAPI.getUserReconciliation(row.user_id)
    detail.value = res.data
  } catch (err: unknown) {
    showError(err)
    detailOpen.value = false
  } finally {
    detailLoading.value = false
  }
}

function closeDetail() {
  detailOpen.value = false
  detail.value = null
  detailUser.value = null
}

function orderStatusLabel(status: string): string {
  const key = `payment.status.${status}`
  return te(key) ? t(key) : status
}

function issueBadgeClass(issue: InvoiceReconciliationIssue): string {
  const base = 'rounded-full px-2 py-0.5 text-xs font-medium'
  return issue === 'duplicate'
    ? `${base} bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300`
    : `${base} bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300`
}

function requestStatusClass(status: InvoiceStatus): string {
  const base = 'rounded-full px-1.5 py-0.5 text-xs font-medium'
  return status === 'completed'
    ? `${base} bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300`
    : `${base} bg-yellow-50 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300`
}

function showError(err: unknown) {
  appStore.showError(extractI18nErrorMessage(err, t, 'invoice.errors', t('common.error')))
}

onMounted(() => {
  search()
})
</script>
