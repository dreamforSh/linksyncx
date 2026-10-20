import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupBadge from '../GroupBadge.vue'
import GroupOptionItem from '../GroupOptionItem.vue'
import type { GroupPlatform } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

describe('GroupOptionItem description layout', () => {
  it('applies multiline and overflow-safe text styles', () => {
    const description = 'First section\nvery-long-unbroken-description-value-that-must-not-overflow'
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Example group',
        platform: 'openai',
        description,
      },
      global: {
        stubs: {
          GroupBadge: true,
        },
      },
    })

    const descriptionElement = wrapper
      .findAll('span')
      .find((element) => element.text() === description)

    expect(descriptionElement).toBeDefined()
    expect(descriptionElement?.classes()).toContain('whitespace-pre-line')
    expect(descriptionElement?.classes()).toContain('[overflow-wrap:anywhere]')
    expect(descriptionElement?.classes()).toContain('line-clamp-3')
    expect(wrapper.find('[title]').attributes('title')).toBe(description)
  })
})

describe('GroupOptionItem rate pill color', () => {
  const ratePill = (platform: GroupPlatform) => {
    const wrapper = mount(GroupOptionItem, {
      props: { name: 'g', platform, rateMultiplier: 1 },
      global: { stubs: { GroupBadge: true } },
    })
    const pill = wrapper.findAll('span').find((s) => s.text().includes('admin.groups.rateLabel'))
    expect(pill, platform).toBeDefined()
    return pill!.classes()
  }

  // 倍率胶囊要与同一选项里 GroupBadge 的平台色同色系，不能都落到 violet 兜底
  it.each([
    ['opencode_go', 'bg-amber-100', 'dark:bg-amber-800/40'],
    ['command_code', 'bg-slate-100', 'dark:bg-slate-500/25'],
    ['grok', 'bg-zinc-100', 'dark:bg-zinc-800'],
    ['perplexity', 'bg-sky-50', 'dark:bg-sky-900/20'],
    ['kimi', 'bg-pink-50', 'dark:bg-pink-900/20'],
    ['zhipu', 'bg-indigo-50', 'dark:bg-indigo-900/20'],
    ['deepseek', 'bg-teal-50', 'dark:bg-teal-900/20'],
    ['minimax', 'bg-rose-50', 'dark:bg-rose-900/20'],
    ['composite', 'bg-cyan-50', 'dark:bg-cyan-900/20'],
  ] as const)('uses the %s badge hue', (platform, lightBg, darkBg) => {
    const classes = ratePill(platform)
    expect(classes).toEqual(expect.arrayContaining([lightBg, darkBg]))
    expect(classes).not.toContain('bg-violet-50')
  })

  it('matches the GroupBadge standard background for the new platforms', () => {
    for (const platform of ['opencode_go', 'command_code'] as const) {
      const badge = mount(GroupBadge, { props: { name: 'g', platform } })
      const badgeBg = badge.classes().filter((c) => /^(dark:)?bg-/.test(c))
      expect(ratePill(platform)).toEqual(expect.arrayContaining(badgeBg))
    }
  })
})
