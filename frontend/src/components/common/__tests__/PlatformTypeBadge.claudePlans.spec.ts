import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import type { AccountPlatform } from '@/types'
import PlatformTypeBadge from '../PlatformTypeBadge.vue'
import { claudePlanTypeLabel } from '@/utils/planType'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

function mountPlan(platform: AccountPlatform, planType: string) {
  return mount(PlatformTypeBadge, {
    props: { platform, type: 'oauth', planType },
  })
}

describe('PlatformTypeBadge Claude plan tiers', () => {
  it('labels the Max tiers with their usage multiplier', () => {
    const max20x = mountPlan('anthropic', 'max_20x')
    expect(max20x.text()).toContain('Max 20x')
    expect(max20x.html()).toContain('bg-orange-100')

    const max5x = mountPlan('anthropic', 'max_5x')
    expect(max5x.text()).toContain('Max 5x')
    expect(max5x.html()).toContain('bg-amber-100')

    const max = mountPlan('anthropic', 'max')
    expect(max.text()).toContain('Max')
    expect(max.html()).toContain('bg-amber-100')
  })

  it('keeps Pro / Team / Free on the shared plan colors', () => {
    const pro = mountPlan('anthropic', 'pro')
    expect(pro.text()).toContain('Pro')
    expect(pro.text()).not.toContain('Pro 20x')
    expect(pro.html()).toContain('bg-violet-100')

    const team = mountPlan('anthropic', 'team')
    expect(team.text()).toContain('Team')
    expect(team.text()).not.toContain('Business')
    expect(team.html()).toContain('bg-indigo-100')

    const free = mountPlan('anthropic', 'free')
    expect(free.text()).toContain('Free')
    expect(free.html()).toContain('bg-gray-100')

    const enterprise = mountPlan('anthropic', 'enterprise')
    expect(enterprise.text()).toContain('Enterprise')
    expect(enterprise.html()).toContain('bg-slate-100')
  })

  it('shows unknown plans verbatim and leaves other platforms alone', () => {
    expect(mountPlan('anthropic', 'student').text()).toContain('student')
    // ChatGPT 的 pro 仍是 Pro 20x，Claude 的档位名不会串到 OpenAI
    expect(mountPlan('openai', 'pro').text()).toContain('Pro 20x')
    expect(mountPlan('openai', 'max_20x').text()).not.toContain('Max 20x')
  })

  it('maps normalized plan types to labels', () => {
    expect(claudePlanTypeLabel('max_20x')).toBe('Max 20x')
    expect(claudePlanTypeLabel('MAX-5X')).toBe('Max 5x')
    expect(claudePlanTypeLabel('enterprise')).toBe('Enterprise')
    expect(claudePlanTypeLabel('')).toBe('')
    expect(claudePlanTypeLabel('student')).toBe('')
  })
})
