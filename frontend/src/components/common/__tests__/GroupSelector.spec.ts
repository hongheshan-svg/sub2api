import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupSelector from '../GroupSelector.vue'

const authState = { isSimpleMode: false }

vi.mock('@/stores', () => ({ useAuthStore: () => authState }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const groups = [
  { id: 1, name: 'Basic', platform: 'anthropic', status: 'active' },
  { id: 2, name: 'Composite', platform: 'composite', status: 'active' }
] as any

const mountSelector = (modelValue: number[] = []) => mount(GroupSelector, {
  props: { modelValue, groups },
  global: { stubs: { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' }, Icon: true } }
})

describe('GroupSelector simple-mode binding policy', () => {
  beforeEach(() => { authState.isSimpleMode = false })

  it('hides composite groups in simple mode and preserves basic groups', () => {
    authState.isSimpleMode = true
    const wrapper = mountSelector()
    expect(wrapper.text()).toContain('Basic')
    expect(wrapper.text()).not.toContain('Composite')
  })

  it('keeps composite groups available in advanced mode', () => {
    const wrapper = mountSelector()
    expect(wrapper.text()).toContain('Composite')
  })

  it('cleans hidden historical composite IDs while preserving visible selections', () => {
    authState.isSimpleMode = true
    const wrapper = mountSelector([1, 2])
    expect(wrapper.emitted('update:modelValue')).toEqual([[[1]]])
  })
})

describe('GroupSelector kiro mixed scheduling', () => {
  beforeEach(() => { authState.isSimpleMode = false })

  const kiroGroups = [
    { id: 1, name: 'Anthropic Default', platform: 'anthropic', status: 'active' },
    { id: 2, name: 'Kiro Only', platform: 'kiro', status: 'active' },
    { id: 3, name: 'Gemini Default', platform: 'gemini', status: 'active' },
    { id: 4, name: 'Composite', platform: 'composite', status: 'active' }
  ] as any

  const mountKiroSelector = (mixedScheduling: boolean) =>
    mount(GroupSelector, {
      props: { modelValue: [], groups: kiroGroups, platform: 'kiro', mixedScheduling },
      global: { stubs: { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' }, Icon: true } }
    })

  it('only shows kiro groups when mixed scheduling is off', () => {
    const wrapper = mountKiroSelector(false)
    expect(wrapper.text()).toContain('Kiro Only')
    expect(wrapper.text()).not.toContain('Anthropic Default')
    expect(wrapper.text()).not.toContain('Gemini Default')
    expect(wrapper.text()).not.toContain('Composite')
  })

  it('additionally shows anthropic groups (but not gemini or composite) once mixed scheduling is on', () => {
    const wrapper = mountKiroSelector(true)
    expect(wrapper.text()).toContain('Kiro Only')
    expect(wrapper.text()).toContain('Anthropic Default')
    expect(wrapper.text()).not.toContain('Gemini Default')
    expect(wrapper.text()).not.toContain('Composite')
  })
})
