import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import { getPlatformTagClass, getPlatformTextClass } from '@/components/admin/channel/types'
import PlatformUsageBreakdown from '@/components/user/PlatformUsageBreakdown.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// 散落在各组件里的按平台着色 / 命名表，新平台不能落到灰色兜底或直接显示原始 id。
describe('channel platform tag/text colors', () => {
  it.each([
    ['minimax', 'rose'],
    ['perplexity', 'sky'],
    ['codebuddy', 'violet'],
    ['opencode_go', 'amber'],
    ['command_code', 'slate'],
    ['grok', 'zinc'],
  ])('colors %s tags with %s', (platform, hue) => {
    expect(getPlatformTagClass(platform)).toContain(`bg-${hue}-100`)
    expect(getPlatformTagClass(platform)).not.toContain('bg-gray-100')
    expect(getPlatformTextClass(platform)).toContain(`text-${hue}-700`)
  })

  it('keeps grok and command_code tags apart', () => {
    expect(getPlatformTagClass('grok')).not.toEqual(getPlatformTagClass('command_code'))
    expect(getPlatformTextClass('grok')).not.toEqual(getPlatformTextClass('command_code'))
  })

  it('keeps the command_code dark tag background off the dark-* surface colours', () => {
    expect(getPlatformTagClass('command_code')).not.toMatch(/dark:bg-slate-(700|800|900)/)
  })
})

describe('PlatformUsageBreakdown platform labels', () => {
  it('names every platform instead of showing the raw id', () => {
    const byPlatform = ['perplexity', 'codebuddy', 'opencode_go', 'command_code'].map((platform, i) => ({
      platform,
      today_actual_cost: 0,
      total_actual_cost: 4 - i,
    }))
    const wrapper = mount(PlatformUsageBreakdown, {
      props: { today: 0, total: 10, byPlatform },
      global: { stubs: { Icon: true } },
    })
    const text = wrapper.text()
    for (const label of ['Perplexity', 'CodeBuddy', 'OpenCode', 'Command Code']) {
      expect(text).toContain(label)
    }
    for (const rawId of ['opencode_go', 'command_code', 'codebuddy']) {
      expect(text).not.toContain(rawId)
    }
  })
})
