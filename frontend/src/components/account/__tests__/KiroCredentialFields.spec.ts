import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import KiroCredentialFields from '../KiroCredentialFields.vue'

describe('KiroCredentialFields', () => {
  it('social 只显示 refreshToken，不显示客户端凭据与 API Key', () => {
    const wrapper = mount(KiroCredentialFields, {
      props: { modelValue: { authMethod: 'social' } }
    })
    expect(wrapper.find('[data-test="kiro-refresh-token"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="kiro-client-id"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="kiro-api-key"]').exists()).toBe(false)
  })

  it('idc 显示 issuer_url 与客户端凭据', () => {
    const wrapper = mount(KiroCredentialFields, {
      props: { modelValue: { authMethod: 'idc' } }
    })
    expect(wrapper.find('[data-test="kiro-issuer-url"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="kiro-client-id"]').exists()).toBe(true)
  })

  it('api_key 只显示密钥输入', () => {
    const wrapper = mount(KiroCredentialFields, {
      props: { modelValue: { authMethod: 'api_key' } }
    })
    expect(wrapper.find('[data-test="kiro-api-key"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="kiro-refresh-token"]').exists()).toBe(false)
  })

  it('假思考开关默认关闭', () => {
    const wrapper = mount(KiroCredentialFields, {
      props: { modelValue: { authMethod: 'social' } }
    })
    const toggle = wrapper.find('[data-test="kiro-fake-thinking"]')
    expect(toggle.exists()).toBe(true)
    expect(toggle.attributes('aria-checked')).not.toBe('true')
  })
})
