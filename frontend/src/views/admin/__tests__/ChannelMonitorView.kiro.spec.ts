import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import MonitorFormDialog from '@/components/admin/monitor/MonitorFormDialog.vue'
import {
  PROVIDERS,
  PROVIDER_KIRO,
  QUOTA_ONLY_PROVIDERS,
} from '@/constants/channelMonitor'

// Kiro 与 Antigravity 共享同一约束：OAuth 凭据形态，没有探活 adapter，
// 只能走 check_mode=quota（复用账号侧 KiroQuotaFetcher 用量数据）。

const { listTemplates, accountsList, accountsGetById } = vi.hoisted(() => ({
  listTemplates: vi.fn(),
  accountsList: vi.fn(),
  accountsGetById: vi.fn(),
}))

vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorV1Mode: () => true,
  isChannelMonitorV2Mode: () => false,
  getChannelMonitorMode: () => 'v1' as const,
}))

vi.mock('@/features/channel-monitor-v2/MonitorSettingsPanel.vue', () => ({
  default: { name: 'MonitorSettingsPanel', template: '<div data-testid="v2-settings" />' },
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelMonitor: {
      create: vi.fn(),
      update: vi.fn(),
    },
    channelMonitorTemplate: {
      list: listTemplates,
    },
    accounts: {
      list: (...args: unknown[]) => accountsList(...args),
      getById: (...args: unknown[]) => accountsGetById(...args),
    },
  },
}))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: vi.fn() },
}))

vi.mock('@/api/groups', () => ({
  userGroupsAPI: { getUserGroupRates: vi.fn() },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: null,
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

function mountDialog() {
  return mount(MonitorFormDialog, {
    props: { show: true, monitor: null },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Toggle: true,
        Select: true,
        ModelTagInput: true,
        MonitorKeyPickerDialog: true,
        MonitorAdvancedRequestConfig: true,
      },
    },
  })
}

describe('channel monitor Kiro provider', () => {
  beforeEach(() => {
    listTemplates.mockReset().mockResolvedValue({ items: [] })
    accountsList.mockReset().mockResolvedValue({ items: [] })
    accountsGetById.mockReset()
  })

  it('is registered as a quota-only provider alongside antigravity', () => {
    expect(PROVIDERS).toContain(PROVIDER_KIRO)
    expect(QUOTA_ONLY_PROVIDERS).toContain(PROVIDER_KIRO)
  })

  it('offers Kiro in the provider grid and forces quota-only check mode', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    const kiroButton = wrapper.get('[data-testid="monitor-provider-kiro"]')
    expect(kiroButton.find('svg').exists()).toBe(true)
    expect(kiroButton.text()).toContain('monitorCommon.providers.kiro')

    await kiroButton.trigger('click')

    // check_mode 被强制切到 quota，probe/quota_probe 按钮禁用。
    expect(wrapper.get('[data-testid="monitor-check-mode-quota"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-testid="monitor-check-mode-probe"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="monitor-check-mode-quota_probe"]').attributes('disabled')).toBeDefined()
  })

  it('restores probe mode when switching away from Kiro', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('[data-testid="monitor-provider-kiro"]').trigger('click')
    expect(wrapper.get('[data-testid="monitor-check-mode-quota"]').attributes('aria-pressed')).toBe('true')

    await wrapper.get('[data-testid="monitor-provider-anthropic"]').trigger('click')
    expect(wrapper.get('[data-testid="monitor-check-mode-probe"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-testid="monitor-check-mode-probe"]').attributes('disabled')).toBeUndefined()
  })
})
