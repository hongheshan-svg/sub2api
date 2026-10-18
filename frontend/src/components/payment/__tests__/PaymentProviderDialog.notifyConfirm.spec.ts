// fork: EasyPay 回调查单确认开关（notifyConfirm），支付FM 等没有 act=order 查单接口的平台需关闭。
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import PaymentProviderDialog from '@/components/payment/PaymentProviderDialog.vue'
import { EASYPAY_NOTIFY_CONFIRM_OPTIONS, PROVIDER_CONFIG_FIELDS } from '@/components/payment/providerConfig'
import type { ProviderInstance } from '@/types/payment'

const messages: Record<string, string> = {
  'admin.settings.payment.notifyConfirmQuery': 'On (recommended)',
  'admin.settings.payment.notifyConfirmOff': 'Off (no query endpoint)',
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
  }),
}))

function easyPayProvider(config: Record<string, string> = {}): ProviderInstance {
  return {
    id: 1,
    provider_key: 'easypay',
    name: 'ZhifuFM',
    config: {
      pid: '4508888888888',
      apiBase: 'https://pay.example.com',
      notifyUrl: 'https://example.com/api/v1/payment/webhook/easypay',
      returnUrl: 'https://example.com/payment/result',
      ...config,
    },
    supported_types: ['alipay'],
    enabled: true,
    payment_mode: 'qrcode',
    refund_enabled: false,
    allow_user_refund: false,
    limits: '',
    sort_order: 0,
  }
}

const SelectStub = { name: 'Select', props: ['modelValue', 'options', 'disabled'], template: '<div />' }

async function mountWithProvider(provider: ProviderInstance) {
  const wrapper = mount(PaymentProviderDialog, {
    props: {
      show: true,
      saving: false,
      editing: provider,
      allKeyOptions: [{ value: 'easypay', label: 'EasyPay' }],
      enabledKeyOptions: [{ value: 'easypay', label: 'EasyPay' }],
      allPaymentTypes: [{ value: 'alipay', label: 'Alipay' }],
      redirectLabel: 'Redirect',
    },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: SelectStub,
        ToggleSwitch: { template: '<div />' },
      },
    },
  })
  ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
  await nextTick()
  return wrapper
}

async function savedConfig(provider: ProviderInstance) {
  const wrapper = await mountWithProvider(provider)
  await wrapper.find('form').trigger('submit')
  const payload = wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }
  wrapper.unmount()
  return payload.config
}

describe('EasyPay notifyConfirm field', () => {
  it('defaults to querying upstream and offers only query / off', () => {
    const field = PROVIDER_CONFIG_FIELDS.easypay.find(f => f.key === 'notifyConfirm')
    expect(field?.defaultValue).toBe('query')
    expect(field?.sensitive).toBe(false)
    expect(field?.hintKey).toBe('admin.settings.payment.field_notifyConfirmHint')
    expect(EASYPAY_NOTIFY_CONFIRM_OPTIONS.map(o => o.value)).toEqual(['query', 'off'])
  })

  it('keeps confirmation on for existing instances that never set it', async () => {
    expect((await savedConfig(easyPayProvider())).notifyConfirm).toBe('query')
  })

  it('preserves a disabled confirmation on save', async () => {
    expect((await savedConfig(easyPayProvider({ notifyConfirm: 'off' }))).notifyConfirm).toBe('off')
  })

  it('renders translated option labels', async () => {
    const wrapper = await mountWithProvider(easyPayProvider())
    const select = wrapper
      .findAllComponents(SelectStub)
      .find(c => (c.props('options') as { value: string }[] | undefined)?.some(o => o.value === 'off'))
    expect(select?.props('options')).toEqual([
      expect.objectContaining({ value: 'query', label: 'On (recommended)' }),
      expect.objectContaining({ value: 'off', label: 'Off (no query endpoint)' }),
    ])
    wrapper.unmount()
  })
})
