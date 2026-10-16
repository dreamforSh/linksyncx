import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import PerplexityOptionsFields from '../PerplexityOptionsFields.vue'
import PerplexityUsageDetails from '../PerplexityUsageDetails.vue'
import {
  applyPerplexityOptions,
  defaultPerplexityOptions,
  readPerplexityOptions,
  validatePerplexityOptions
} from '../credentialsBuilder'
import { getPresetMappingsByPlatform } from '@/composables/useModelWhitelist'
import type { PerplexityRequestOptions } from '@/types'

const i18n = createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} })

function readComponent(name: string): string {
  return readFileSync(resolve(process.cwd(), `src/components/account/${name}.vue`), 'utf8')
}

describe('Perplexity search settings contract', () => {
  it('keeps generic client compatibility by default and serializes only public keys', () => {
    const options = defaultPerplexityOptions()
    expect(options).toMatchObject({ sources: ['web'], include_sources: true, include_search_progress: true, native_followups: true, strict_parameters: false })
    const credentials: Record<string, unknown> = { api_key: 'existing' }
    applyPerplexityOptions(credentials, {
      ...options,
      language: ' zh-CN ',
      sources: ['web', 'scholar', 'web'],
      internal: { last_backend_uuid: 'not-public' },
      mode: 'computer'
    } as PerplexityRequestOptions)
    expect(credentials.api_key).toBe('existing')
    expect(credentials.perplexity_options).toEqual({ ...options, language: 'zh-CN', sources: ['web', 'scholar'] })
  })

  it('reads saved false booleans and restores defaults for omitted fields', () => {
    const options = readPerplexityOptions({ perplexity_options: { include_sources: false, native_followups: false, sources: ['social'], language: 'fr-FR' } })
    expect(options).toMatchObject({ sources: ['social'], language: 'fr-FR', include_sources: false, native_followups: false, include_search_progress: true, strict_parameters: false })
  })

  it('rejects empty sources, malformed locales and invalid timezones', () => {
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), sources: [] })).toBe('sourcesRequired')
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), language: 'zh_CN' })).toBe('invalidLanguage')
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), timezone: 'Invalid/Timezone' })).toBe('invalidTimezone')
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), language: 'zh-CN', timezone: 'Asia/Shanghai' })).toBeNull()
  })

  it('emits checkbox and language changes without mutating parent state', async () => {
    const options = defaultPerplexityOptions()
    const wrapper = mount(PerplexityOptionsFields, { props: { modelValue: options }, global: { plugins: [i18n] } })
    await wrapper.get('[data-testid="perplexity-source-scholar"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toMatchObject({ sources: ['web', 'scholar'] })
    expect(options.sources).toEqual(['web'])
    await wrapper.get('[data-testid="perplexity-language"]').setValue('zh-CN')
    expect(wrapper.emitted('update:modelValue')?.[1]?.[0]).toMatchObject({ language: 'zh-CN' })
    await wrapper.get('[data-testid="perplexity-strict_parameters"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[2]?.[0]).toMatchObject({ strict_parameters: true })
  })

  it('uses the shared fields and allowlisted serializer on both create and edit', () => {
    expect(readComponent('CreateAccountModal')).toContain('v-model="perplexityOptions"')
    expect(readComponent('CreateAccountModal')).toContain('applyPerplexityOptions(credentials, perplexityOptions.value)')
    expect(readComponent('EditAccountModal')).toContain('v-model="editPerplexityOptions"')
    expect(readComponent('EditAccountModal')).toContain('readPerplexityOptions(newAccount.credentials)')
    expect(readComponent('EditAccountModal')).toContain('applyPerplexityOptions(newCredentials, editPerplexityOptions.value)')
  })

  it('carries the selected proxy and CSRF cookie into the create-flow model preview', () => {
    const source = readComponent('CreateAccountModal')
    expect(source).toContain('proxy_id: form.proxy_id')
    expect(source).toContain('csrf_token: perplexityCsrfToken.value.trim()')
    expect(source).toContain('form.proxy_id != null')
  })

  it('offers dedicated Perplexity presets rather than Anthropic fallbacks', () => {
    const presets = getPresetMappingsByPlatform('perplexity')
    expect(presets.map(preset => preset.from)).toContain('sonar-pro')
    expect(presets.some(preset => preset.to === 'pplx_pro')).toBe(true)
    expect(presets.some(preset => preset.from.startsWith('claude-'))).toBe(false)
  })
})

describe('Perplexity usage snapshots', () => {
  it('shows per-section last-good times, stale values and unknown failed sections without claiming live usage', () => {
    const wrapper = mount(PerplexityUsageDetails, {
      props: { usage: {
        snapshot: { credits: { balance: 7 }, session: { subscription_tier: 'pro' } },
        sections: {
          session: { status: 'success', fetched_at: '2026-10-16T00:00:00Z' },
          credits: { status: 'stale', fetched_at: '2026-10-15T00:00:00Z', attempted_at: '2026-10-16T00:00:00Z', error: 'credits timeout' },
          rate_limits: { status: 'error', error: 'limits unavailable' }
        }
      } },
      global: { plugins: [i18n] }
    })
    expect(wrapper.get('[data-testid="perplexity-session-status"]').text()).toContain('sessionUnknown')
    const credits = wrapper.get('[data-testid="perplexity-usage-credits"]')
    expect(credits.text()).toContain('stale')
    expect(credits.text()).toContain('balance: 7')
    expect(credits.text()).toContain('credits timeout')
    expect(credits.find('[title="2026-10-15T00:00:00Z"]').exists()).toBe(true)
    const limits = wrapper.get('[data-testid="perplexity-usage-rate_limits"]')
    expect(limits.text()).toContain('unknown')
    expect(limits.text()).toContain('limits unavailable')
    expect(limits.find('pre').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('0%')
  })

  it('treats a failed refresh as unknown session status and retained values as stale', () => {
    const wrapper = mount(PerplexityUsageDetails, {
      props: {
        unavailable: true,
        usage: { session_valid: true, snapshot: { credits: { balance: 7 } }, sections: { credits: { status: 'success', fetched_at: '2026-10-15T00:00:00Z' } } }
      },
      global: { plugins: [i18n] }
    })
    expect(wrapper.get('[data-testid="perplexity-session-status"]').text()).toContain('sessionUnknown')
    expect(wrapper.get('[data-testid="perplexity-usage-credits"]').text()).toContain('stale')
    expect(wrapper.get('[data-testid="perplexity-usage-credits"]').text()).toContain('balance: 7')
  })

  it('only declares session invalid when the backend explicitly says false', async () => {
    const wrapper = mount(PerplexityUsageDetails, { props: { usage: { snapshot: {}, sections: {}, session_valid: false } }, global: { plugins: [i18n] } })
    expect(wrapper.get('[data-testid="perplexity-session-status"]').text()).toContain('sessionInvalid')
    await wrapper.setProps({ usage: { snapshot: {}, sections: {} } })
    expect(wrapper.get('[data-testid="perplexity-session-status"]').text()).toContain('sessionUnknown')
  })
})
