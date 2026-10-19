import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'

const isV1 = vi.fn(() => false)
const isV2 = vi.fn(() => true)

vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorV1Mode: () => isV1(),
  isChannelMonitorV2Mode: () => isV2(),
}))

vi.mock('../ChannelStatusV1View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV1View', setup: () => () => h('div', { 'data-testid': 'v1' }) }),
}))
vi.mock('../ChannelStatusV2View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV2View', setup: () => () => h('div', { 'data-testid': 'v2' }) }),
}))
vi.mock('../ChannelStatusV3View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV3View', setup: () => () => h('div', { 'data-testid': 'v3' }) }),
}))

import ChannelStatusView from '../ChannelStatusView.vue'

describe('ChannelStatusView mode switch', () => {
  beforeEach(() => {
    isV1.mockReset()
    isV2.mockReset()
    isV1.mockReturnValue(false)
    isV2.mockReturnValue(true)
    window.history.replaceState({}, '', '/')
  })

  it('renders V3 by default in v2 mode', () => {
    const wrapper = mount(ChannelStatusView)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
  })

  it('renders legacy V2 when monitor_view=v2 is requested', () => {
    window.history.replaceState({}, '', '/channel-status?monitor_view=v2')
    const wrapper = mount(ChannelStatusView)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(false)
  })

  it('renders V1 when in v1 mode', () => {
    isV1.mockReturnValue(true)
    const wrapper = mount(ChannelStatusView)
    expect(wrapper.find('[data-testid="v1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(false)
  })

  it('falls back to V3 when neither monitor mode flag is active', () => {
    isV2.mockReturnValue(false)
    const wrapper = mount(ChannelStatusView)
    expect(wrapper.find('[data-testid="v3"]').exists()).toBe(true)
  })
})
