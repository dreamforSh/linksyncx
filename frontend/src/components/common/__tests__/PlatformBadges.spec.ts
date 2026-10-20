import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupBadge from '../GroupBadge.vue'
import PlatformTypeBadge from '../PlatformTypeBadge.vue'
import type { GroupPlatform, SubscriptionType } from '@/types'

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

type BadgeProps = {
  platform: GroupPlatform
  subscriptionType?: SubscriptionType
  daysRemaining?: number | null
  peakRateEnabled?: boolean
  peakStart?: string
  peakEnd?: string
}

const mountGroup = (props: BadgeProps) =>
  mount(GroupBadge, {
    props: { name: 'g', subscriptionType: 'standard', rateMultiplier: 1, ...props },
  })

// 只取背景色类（含 dark: 变体），用来比较两个元素的底色是否撞色
const bgClasses = (classes: string[]) => classes.filter((c) => /^(dark:)?bg-/.test(c)).sort()

const findSpanByText = (wrapper: ReturnType<typeof mountGroup>, text: string) => {
  const span = wrapper.findAll('span').find((s) => s.text() === text)
  expect(span, `span "${text}"`).toBeDefined()
  return span!
}

// dark-* 色阶就是 slate（dark-700 = 下拉悬停、dark-800 = 卡片、dark-900 = 页面），
// command_code 的暗色底不能用 slate-700/800/900，否则会糊进这些表面。
const SURFACE_COLLIDING_DARK_BG = /^dark:bg-slate-(700|800|900)(\/\d+)?$/

// OpenCode Go = amber、Command Code = slate（与 platformColors.ts / GroupsView 同色系），
// 两个徽章都不能落到默认的 emerald/violet/blue 兜底色。
describe('GroupBadge platform colors', () => {
  it('uses amber for opencode_go in both standard and subscription groups', () => {
    const standard = mountGroup({ platform: 'opencode_go' })
    expect(standard.classes()).toEqual(
      expect.arrayContaining(['bg-amber-100', 'text-amber-800', 'dark:bg-amber-800/40', 'dark:text-amber-300'])
    )
    expect(standard.classes()).not.toContain('bg-emerald-100')

    const subscription = mountGroup({ platform: 'opencode_go', subscriptionType: 'subscription' })
    expect(subscription.classes()).toEqual(
      expect.arrayContaining(['bg-amber-200', 'text-amber-900', 'dark:bg-amber-800/60', 'dark:text-amber-200'])
    )
    expect(subscription.classes()).not.toContain('bg-violet-100')
    // 订阅分组右侧 label 也跟随平台色
    const label = findSpanByText(subscription, 'groups.subscription')
    expect(label.classes()).toEqual(expect.arrayContaining(['bg-amber-300/70', 'dark:bg-amber-500/30']))
    expect(subscription.html()).not.toContain('bg-violet-200/60')
  })

  it('keeps opencode_go standard groups visually apart from anthropic standard groups', () => {
    const opencode = bgClasses(mountGroup({ platform: 'opencode_go' }).classes())
    const anthropic = bgClasses(mountGroup({ platform: 'anthropic' }).classes())
    // 浅色、暗色两套底色都不能与 anthropic 标准分组（amber-50 / amber-900/20）重合
    expect(opencode.filter((c) => anthropic.includes(c))).toEqual([])
  })

  it('keeps the opencode_go subscription and standard backgrounds distinct', () => {
    const standard = bgClasses(mountGroup({ platform: 'opencode_go' }).classes())
    const subscription = bgClasses(mountGroup({ platform: 'opencode_go', subscriptionType: 'subscription' }).classes())
    expect(standard.filter((c) => subscription.includes(c))).toEqual([])
  })

  it('gives the opencode_go expiry warning label a different hue from its normal label', () => {
    const normal = findSpanByText(
      mountGroup({ platform: 'opencode_go', subscriptionType: 'subscription', daysRemaining: 30 }),
      'admin.users.daysRemaining'
    )
    const warningBadge = mountGroup({ platform: 'opencode_go', subscriptionType: 'subscription', daysRemaining: 5 })
    const warning = findSpanByText(warningBadge, 'admin.users.daysRemaining')

    expect(normal.classes()).toEqual(expect.arrayContaining(['bg-amber-300/70', 'text-amber-900']))
    expect(warning.classes()).toEqual(
      expect.arrayContaining(['bg-orange-300/80', 'text-orange-900', 'dark:bg-orange-800/60', 'dark:text-orange-300'])
    )
    // 不只是透明度不同：浅色、暗色的底色和文字色都要换，且警告底色不能再是 amber
    const colorHues = (classes: string[]) =>
      classes.map((c) => /^((?:dark:)?(?:bg|text))-([a-z]+)-\d/.exec(c)).filter(Boolean).map((m) => `${m![1]}-${m![2]}`)
    expect(colorHues(warning.classes()).filter((h) => colorHues(normal.classes()).includes(h))).toEqual([])
    expect(colorHues(warning.classes()).filter((h) => colorHues(warningBadge.classes()).includes(h))).toEqual([])
  })

  it('leaves the shared amber expiry warning unchanged for other platforms', () => {
    const warning = findSpanByText(
      mountGroup({ platform: 'kimi', subscriptionType: 'subscription', daysRemaining: 5 }),
      'admin.users.daysRemaining'
    )
    expect(warning.classes()).toEqual(expect.arrayContaining(['bg-amber-200/80', 'text-amber-800']))
  })

  it('keeps the peak-rate pill distinguishable on opencode_go badges', () => {
    const peakProps = { peakRateEnabled: true, peakStart: '09:00', peakEnd: '18:00' }
    for (const subscriptionType of ['standard', 'subscription'] as const) {
      const wrapper = mountGroup({ platform: 'opencode_go', subscriptionType, ...peakProps })
      const pill = wrapper.find('span[title="common.peakRateTooltip"]')
      expect(pill.exists()).toBe(true)
      const pillBg = bgClasses(pill.classes())
      const badgeBg = bgClasses(wrapper.classes())
      expect(pillBg.filter((c) => badgeBg.includes(c)), subscriptionType).toEqual([])
      // 同为 amber 系，额外靠描边给出边界
      expect(pill.classes()).toEqual(expect.arrayContaining(['ring-1', 'ring-amber-300', 'dark:ring-amber-600/50']))
    }

    // 其它平台的高峰标签保持原样
    const anthropicPill = mountGroup({ platform: 'anthropic', ...peakProps }).find('span[title="common.peakRateTooltip"]')
    expect(anthropicPill.classes()).toEqual(expect.arrayContaining(['bg-amber-100', 'text-amber-700']))
    expect(anthropicPill.classes()).not.toContain('ring-1')
  })

  it('uses slate for command_code in both standard and subscription groups', () => {
    const standard = mountGroup({ platform: 'command_code' })
    expect(standard.classes()).toEqual(
      expect.arrayContaining(['bg-slate-100', 'text-slate-700', 'dark:bg-slate-500/25', 'dark:text-slate-300'])
    )
    expect(standard.classes()).not.toContain('bg-emerald-100')

    const subscription = mountGroup({ platform: 'command_code', subscriptionType: 'subscription' })
    expect(subscription.classes()).toEqual(
      expect.arrayContaining(['bg-slate-200', 'text-slate-800', 'dark:bg-slate-500/50', 'dark:text-slate-100'])
    )
    expect(subscription.classes()).not.toContain('bg-violet-100')
    const label = findSpanByText(subscription, 'groups.subscription')
    expect(label.classes()).toEqual(expect.arrayContaining(['bg-slate-300/70', 'dark:bg-slate-400/40']))
    expect(subscription.html()).not.toContain('bg-violet-200/60')
  })

  it('keeps command_code dark backgrounds off the dark-700/800/900 surface colours', () => {
    for (const subscriptionType of ['standard', 'subscription'] as const) {
      const wrapper = mountGroup({ platform: 'command_code', subscriptionType })
      const all = wrapper.findAll('span').flatMap((s) => s.classes())
      expect(all.filter((c) => SURFACE_COLLIDING_DARK_BG.test(c)), subscriptionType).toEqual([])
    }
  })
})

describe('PlatformTypeBadge platform colors', () => {
  const segments = (platform: 'opencode_go' | 'command_code') => {
    const wrapper = mount(PlatformTypeBadge, { props: { platform, type: 'apikey' } })
    const [platformSegment, typeSegment] = Array.from(wrapper.element.firstElementChild!.children)
    return { wrapper, platformSegment, typeSegment }
  }

  it('uses amber for opencode_go', () => {
    const { wrapper, platformSegment, typeSegment } = segments('opencode_go')
    expect(wrapper.text()).toContain('OpenCode')
    expect(Array.from(platformSegment.classList)).toEqual(
      expect.arrayContaining(['bg-amber-100', 'text-amber-800', 'dark:bg-amber-900/30', 'dark:text-amber-300'])
    )
    expect(Array.from(typeSegment.classList)).toEqual(
      expect.arrayContaining(['bg-amber-100', 'text-amber-700', 'dark:bg-amber-900/30', 'dark:text-amber-300'])
    )
    expect(wrapper.html()).not.toContain('bg-blue-100')
  })

  it('uses slate for command_code', () => {
    const { wrapper, platformSegment, typeSegment } = segments('command_code')
    expect(wrapper.text()).toContain('Command Code')
    expect(Array.from(platformSegment.classList)).toEqual(
      expect.arrayContaining(['bg-slate-100', 'text-slate-700', 'dark:bg-slate-500/25', 'dark:text-slate-300'])
    )
    expect(Array.from(typeSegment.classList)).toEqual(
      expect.arrayContaining(['bg-slate-100', 'text-slate-600', 'dark:bg-slate-500/25', 'dark:text-slate-300'])
    )
    const all = [...Array.from(platformSegment.classList), ...Array.from(typeSegment.classList)]
    expect(all.filter((c) => SURFACE_COLLIDING_DARK_BG.test(c))).toEqual([])
    expect(wrapper.html()).not.toContain('bg-blue-100')
  })
})
