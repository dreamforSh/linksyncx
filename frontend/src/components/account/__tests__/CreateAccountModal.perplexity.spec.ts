import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { applyPerplexitySession } from '../credentialsBuilder'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

describe('CreateAccountModal Perplexity web-session accounts', () => {
  it('offers a Perplexity platform option that forces the apikey account type', () => {
    expect(source).toContain('data-testid="platform-perplexity"')
    expect(source).toContain('@click="selectPerplexityPlatform()"')
    expect(source).toContain('function selectPerplexityPlatform()')
    expect(source).toContain("form.platform = 'perplexity'")
    expect(source).toContain("accountCategory.value = 'apikey'")
    // 分组预选平台时同样走强制 apikey 的选择函数
    expect(source).toContain("platform === 'perplexity') selectPerplexityPlatform()")
  })

  it('shows the session token field (with cookie hint) and optional CSRF token field', () => {
    // 会话令牌复用 API Key 输入框，perplexity 下打上专属 testid 与文案
    expect(source).toContain("'perplexity-session-token-input'")
    expect(source).toContain('data-testid="perplexity-csrf-token-input"')
    expect(source).toContain("t('admin.accounts.perplexity.sessionToken')")
    expect(source).toContain("t('admin.accounts.perplexity.sessionTokenHint')")
    expect(source).toContain("t('admin.accounts.perplexity.csrfToken')")
    // Base URL 占位符回落官方站点
    expect(source).toContain('PERPLEXITY_DEFAULT_BASE_URL')
  })

  it('fails validation when the session token is empty', () => {
    expect(source).toContain("t('admin.accounts.perplexity.sessionTokenRequired')")
    expect(source).toContain('if (!apiKeyValue.value.trim())')
  })

  it('assembles credentials via applyPerplexitySession on the create path', () => {
    expect(source).toContain('applyPerplexitySession(')
    expect(source).toContain('sessionToken: apiKeyValue.value')
    expect(source).toContain('csrfToken: perplexityCsrfToken.value')
    expect(source).toContain('baseUrl: apiKeyBaseUrl.value')
    expect(source).toContain("'create'")
  })

  it('does not offer any OAuth flow for Perplexity', () => {
    // 没有 perplexity 专属的 OAuth 区块/分支：OAuth 区块均以 isOAuthFlow 门控，
    // 而 perplexity 强制 accountCategory = apikey（isOAuthFlow 恒为 false）
    expect(source).not.toContain("platform === 'perplexity' && isOAuthFlow")
    expect(source).not.toContain("form.platform === 'perplexity' && isOAuthFlow")
  })
})

describe('applyPerplexitySession', () => {
  it('create: writes api_key plus optional csrf_token / base_url (trimmed)', () => {
    const creds: Record<string, unknown> = {}
    applyPerplexitySession(
      creds,
      { sessionToken: '  sess-token  ', csrfToken: ' csrf ', baseUrl: ' https://pplx.example.com ' },
      'create'
    )
    expect(creds).toEqual({
      api_key: 'sess-token',
      csrf_token: 'csrf',
      base_url: 'https://pplx.example.com'
    })
  })

  it('create: omits csrf_token / base_url when empty so the backend default applies', () => {
    const creds: Record<string, unknown> = {}
    applyPerplexitySession(creds, { sessionToken: 'sess', csrfToken: '', baseUrl: '   ' }, 'create')
    expect(creds).toEqual({ api_key: 'sess' })
  })

  it('create: never deletes api_key even when the session token is empty', () => {
    const creds: Record<string, unknown> = { api_key: 'preset' }
    applyPerplexitySession(creds, { sessionToken: ' ', csrfToken: '', baseUrl: '' }, 'create')
    expect(creds.api_key).toBe('preset')
  })

  it('edit: empty csrf_token / base_url delete the keys (empty session token keeps nothing set)', () => {
    const creds: Record<string, unknown> = {
      api_key: 'old-sess',
      csrf_token: 'old-csrf',
      base_url: 'https://old.example.com'
    }
    applyPerplexitySession(creds, { sessionToken: '', csrfToken: '', baseUrl: '' }, 'edit')
    expect('api_key' in creds).toBe(false)
    expect('csrf_token' in creds).toBe(false)
    expect('base_url' in creds).toBe(false)
  })

  it('edit: rotates provided values and deletes only the cleared optional keys', () => {
    const creds: Record<string, unknown> = {
      csrf_token: 'old-csrf',
      base_url: 'https://old.example.com'
    }
    applyPerplexitySession(
      creds,
      { sessionToken: 'new-sess', csrfToken: '', baseUrl: 'https://new.example.com' },
      'edit'
    )
    expect(creds).toEqual({ api_key: 'new-sess', base_url: 'https://new.example.com' })
  })
})
