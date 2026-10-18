/**
 * Admin CodeBuddy / WorkBuddy (Tencent) API endpoints.
 * Scan login on the domestic or international site, credential import,
 * re-authorization, catalog sync and credits.
 */

import { apiClient } from '../client'

/** Login site: domestic (codebuddy.cn), international WorkBuddy, international CodeBuddy. */
export type CodeBuddyLoginSite = 'cn' | 'intl' | 'intl-codebuddy'

/** Product profile (region × product) a credential belongs to. */
export type CodeBuddyProfile = 'cn-cli' | 'cn-work' | 'intl-cli' | 'intl-work'

export type CodeBuddyCreditPolicy = 'all' | 'zero_only'

export interface CodeBuddyStartResponse {
  login_id: string
  verification_uri: string
  expires_in: number
}

export interface CodeBuddyPollResponse {
  done: boolean
  error?: string
  uid?: string
  nickname?: string
  profile?: CodeBuddyProfile
}

/** Exactly one credential source: a finished scan login, a pasted .info document, or a refresh token. */
export interface CodeBuddyCredentialSource {
  login_id?: string
  credentials?: Record<string, unknown>
  refresh_token?: string
  /** Required with refresh_token: a domestic token cannot tell CLI from WorkBuddy. */
  profile?: CodeBuddyProfile
}

export interface CodeBuddyCreateRequest extends CodeBuddyCredentialSource {
  credit_policy?: CodeBuddyCreditPolicy
  model_mapping?: Record<string, unknown>
  proxy_id?: number | null
  name?: string
  notes?: string | null
  concurrency?: number
  priority?: number
  rate_multiplier?: number
  load_factor?: number
  group_ids?: number[]
}

export async function startLogin(site: CodeBuddyLoginSite, proxyId?: number | null): Promise<CodeBuddyStartResponse> {
  const payload: Record<string, unknown> = { site }
  if (proxyId) payload.proxy_id = proxyId
  const { data } = await apiClient.post<CodeBuddyStartResponse>('/admin/codebuddy/oauth/start', payload)
  return data
}

export async function pollLogin(loginId: string): Promise<CodeBuddyPollResponse> {
  const { data } = await apiClient.post<CodeBuddyPollResponse>('/admin/codebuddy/oauth/poll', {
    login_id: loginId
  })
  return data
}

export async function createFromOAuth(payload: CodeBuddyCreateRequest) {
  const { data } = await apiClient.post('/admin/codebuddy/oauth/create-from-oauth', payload)
  return data
}

/**
 * Replaces an account's credentials from a fresh login, keeping its groups,
 * priority and usage history. The backend rejects a different CodeBuddy uid.
 */
export async function reAuthAccount(id: number, payload: CodeBuddyCredentialSource) {
  const { data } = await apiClient.post(`/admin/codebuddy/accounts/${id}/reauth`, payload)
  return data
}

export async function refreshAccountToken(id: number) {
  const { data } = await apiClient.post(`/admin/codebuddy/accounts/${id}/refresh`)
  return data
}

export interface CodeBuddyCatalogModel {
  id: string
  name?: string
  aliases?: string[]
  credits?: string
  credits_value?: number
}

export async function syncModels(id: number): Promise<{ models: string[]; catalog?: CodeBuddyCatalogModel[] }> {
  const { data } = await apiClient.post(`/admin/codebuddy/accounts/${id}/sync-models`)
  return data
}

export interface CodeBuddyCreditSegment {
  remaining: number
  total: number
  expires_at?: number
  source?: string
  package_code?: string
}

export interface CodeBuddyCreditsSnapshot {
  credits: number
  total?: number
  count?: number
  segments?: CodeBuddyCreditSegment[]
  soonest_expiry?: number
  intl?: boolean
  /** Enterprise tenants carry one cycle allowance instead of packages. */
  enterprise?: boolean
  unlimited?: boolean
  used?: number
  reset_at?: number
  fetched_at?: number
  /** Drawn down locally since the last billing probe. */
  estimated?: boolean
  /** The upstream rejected a request for lack of credits. */
  exhausted?: boolean
}

/** Gateway-side tally; CodeBuddy charges per request, not per token. */
export interface CodeBuddyCreditsUsage {
  day: string
  requests: number
  credits: number
  /** Requests whose model was absent from the catalog, so `credits` is a floor. */
  unpriced?: number
  total_requests: number
  total_credits: number
  updated_at: number
}

export interface CodeBuddyRequestUsageDay {
  day: string
  credits: number
  requests: number
  by_model?: Record<string, number>
}

/** CodeBuddy's own billing report: also covers the official IDE and other clients. */
export interface CodeBuddyRequestUsage {
  days: CodeBuddyRequestUsageDay[]
  total_credits: number
  requests: number
  range_days: number
  fetched_at: number
}

export interface CodeBuddyCreditsReport extends Partial<CodeBuddyCreditsSnapshot> {
  usage: CodeBuddyCreditsUsage
  official?: CodeBuddyRequestUsage
  /** The balance came from a cached probe older than the refresh window. */
  stale: boolean
  profile?: CodeBuddyProfile
}

/** Reads an account's credit standing; `refresh` probes the billing API. */
export async function queryCredits(id: number, refresh = true): Promise<CodeBuddyCreditsReport> {
  const { data } = await apiClient.get<CodeBuddyCreditsReport>(`/admin/codebuddy/accounts/${id}/credits`, {
    params: { refresh }
  })
  return data
}

/** Upstream silently empties a window wider than 31 days. */
export const REQUEST_USAGE_MAX_DAYS = 30

export async function queryRequestUsage(id: number, days = REQUEST_USAGE_MAX_DAYS): Promise<CodeBuddyRequestUsage> {
  const { data } = await apiClient.get<CodeBuddyRequestUsage>(`/admin/codebuddy/accounts/${id}/request-usage`, {
    params: { days }
  })
  return data
}

const codebuddyAPI = {
  startLogin,
  pollLogin,
  createFromOAuth,
  reAuthAccount,
  refreshAccountToken,
  syncModels,
  queryCredits,
  queryRequestUsage
}

export default codebuddyAPI
