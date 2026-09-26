/**
 * Admin Clash proxy pool API endpoints
 * Subscriptions (profiles), materialized nodes, account exit options, core runtime and pool settings.
 */

import { apiClient } from '../client'
import type {
  ClashCreateProfileResult,
  ClashExitList,
  ClashExitProbeResult,
  ClashLatencyResult,
  ClashNode,
  ClashNodeListFilters,
  ClashNodeSelection,
  ClashPoolSettings,
  ClashPreviewResult,
  ClashProfile,
  ClashProfileInput,
  ClashRefreshResult,
  ClashRuntimeStatus,
  PaginatedResponse
} from '@/types'

/** Fetching and parsing a subscription happens synchronously on the server. */
export const CLASH_SUBSCRIPTION_TIMEOUT_MS = 120_000
/** Latency tests run through the core (a few seconds per node, concurrently). */
export const CLASH_LATENCY_TIMEOUT_MS = 120_000
/** Exit probes resolve the egress IP and platform reachability of each node. */
export const CLASH_EXIT_PROBE_TIMEOUT_MS = 180_000
/** Re-rendering and reloading the core configuration. */
export const CLASH_RESYNC_TIMEOUT_MS = 60_000
/** The server accepts at most this many node_ids per test/probe request. */
export const CLASH_NODE_BATCH_LIMIT = 50

const forceParams = (force?: boolean) => (force ? { force: 'true' } : undefined)

export async function listProfiles(): Promise<ClashProfile[]> {
  const { data } = await apiClient.get<ClashProfile[]>('/admin/clash/profiles')
  return Array.isArray(data) ? data : []
}

export async function getProfile(id: number): Promise<ClashProfile> {
  const { data } = await apiClient.get<ClashProfile>(`/admin/clash/profiles/${id}`)
  return data
}

/** Creates a subscription; when enabled the server refreshes it once before answering. */
export async function createProfile(input: ClashProfileInput): Promise<ClashCreateProfileResult> {
  const { data } = await apiClient.post<ClashCreateProfileResult>('/admin/clash/profiles', input, {
    timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS
  })
  return data
}

/** All fields optional: an empty url keeps the stored one, fetch_proxy_id=0 clears it. */
export async function updateProfile(id: number, input: ClashProfileInput): Promise<ClashProfile> {
  const { data } = await apiClient.put<ClashProfile>(`/admin/clash/profiles/${id}`, input)
  return data
}

/** Without force the server answers 409 CLASH_PROFILE_IN_USE while accounts use its exits. */
export async function deleteProfile(id: number, options?: { force?: boolean }): Promise<{ deleted: boolean }> {
  const { data } = await apiClient.delete<{ deleted: boolean }>(`/admin/clash/profiles/${id}`, {
    params: forceParams(options?.force)
  })
  return data
}

/** force=true bypasses the node-count drop protection (status "skipped"). */
export async function refreshProfile(id: number, options?: { force?: boolean }): Promise<ClashRefreshResult> {
  const { data } = await apiClient.post<ClashRefreshResult>(`/admin/clash/profiles/${id}/refresh`, undefined, {
    params: forceParams(options?.force),
    timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS
  })
  return data
}

/** Dry-run fetch + parse without saving anything. */
export async function previewProfile(
  input: Pick<ClashProfileInput, 'url' | 'user_agent' | 'include_pattern' | 'exclude_pattern' | 'fetch_proxy_id'>
): Promise<ClashPreviewResult> {
  const { data } = await apiClient.post<ClashPreviewResult>('/admin/clash/profiles/preview', input, {
    timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS
  })
  return data
}

export async function listNodes(
  page: number = 1,
  pageSize: number = 20,
  filters?: ClashNodeListFilters,
  options?: { signal?: AbortSignal }
): Promise<PaginatedResponse<ClashNode>> {
  const params: Record<string, string | number> = { page, page_size: pageSize }
  if (filters?.profile_id) params.profile_id = filters.profile_id
  if (filters?.status) params.status = filters.status
  if (filters?.health) params.health = filters.health
  if (typeof filters?.bound === 'boolean') params.bound = filters.bound ? 'true' : 'false'
  const search = filters?.search?.trim()
  if (search) params.search = search
  const { data } = await apiClient.get<PaginatedResponse<ClashNode>>('/admin/clash/nodes', {
    params,
    signal: options?.signal
  })
  return data
}

export async function enableNode(id: number): Promise<{ enabled: boolean }> {
  const { data } = await apiClient.post<{ enabled: boolean }>(`/admin/clash/nodes/${id}/enable`)
  return data
}

/** Disabling drops the node's connections; bound accounts get paused. */
export async function disableNode(id: number): Promise<{ enabled: boolean }> {
  const { data } = await apiClient.post<{ enabled: boolean }>(`/admin/clash/nodes/${id}/disable`)
  return data
}

/** Confirms an in-place egress IP change; bound accounts stay paused until then. */
export async function acceptNodeExit(id: number): Promise<{ accepted: boolean }> {
  const { data } = await apiClient.post<{ accepted: boolean }>(`/admin/clash/nodes/${id}/accept-exit`)
  return data
}

/**
 * Splits explicit node selections into server-sized batches (sequential requests,
 * results concatenated in order). A profile-wide selection is a single request.
 */
async function runNodeBatches<T>(
  selection: ClashNodeSelection,
  send: (body: ClashNodeSelection) => Promise<T[]>
): Promise<T[]> {
  const ids = selection.node_ids
  if (!ids) return send(selection)
  if (ids.length === 0) return []
  const results: T[] = []
  for (let start = 0; start < ids.length; start += CLASH_NODE_BATCH_LIMIT) {
    const batch = await send({ node_ids: ids.slice(start, start + CLASH_NODE_BATCH_LIMIT) })
    if (Array.isArray(batch)) results.push(...batch)
  }
  return results
}

export async function testNodesLatency(selection: ClashNodeSelection): Promise<ClashLatencyResult[]> {
  return runNodeBatches(selection, async (body) => {
    const { data } = await apiClient.post<ClashLatencyResult[]>('/admin/clash/nodes/test-latency', body, {
      timeout: CLASH_LATENCY_TIMEOUT_MS
    })
    return data
  })
}

export async function probeNodesExit(selection: ClashNodeSelection): Promise<ClashExitProbeResult[]> {
  return runNodeBatches(selection, async (body) => {
    const { data } = await apiClient.post<ClashExitProbeResult[]>('/admin/clash/nodes/probe-exit', body, {
      timeout: CLASH_EXIT_PROBE_TIMEOUT_MS
    })
    return data
  })
}

/** Exit options for the account proxy selector (occupancy is aggregated per egress IP). */
export async function listExits(): Promise<ClashExitList> {
  const { data } = await apiClient.get<ClashExitList>('/admin/clash/exits')
  return data
}

export async function getRuntime(): Promise<ClashRuntimeStatus> {
  const { data } = await apiClient.get<ClashRuntimeStatus>('/admin/clash/runtime')
  return data
}

export async function resyncRuntime(): Promise<ClashRuntimeStatus> {
  const { data } = await apiClient.post<ClashRuntimeStatus>('/admin/clash/runtime/resync', undefined, {
    timeout: CLASH_RESYNC_TIMEOUT_MS
  })
  return data
}

export async function getSettings(): Promise<ClashPoolSettings> {
  const { data } = await apiClient.get<ClashPoolSettings>('/admin/clash/settings')
  return data
}

export async function updateSettings(settings: ClashPoolSettings): Promise<ClashPoolSettings> {
  const { data } = await apiClient.put<ClashPoolSettings>('/admin/clash/settings', settings)
  return data
}

export const clashAPI = {
  listProfiles,
  getProfile,
  createProfile,
  updateProfile,
  deleteProfile,
  refreshProfile,
  previewProfile,
  listNodes,
  enableNode,
  disableNode,
  acceptNodeExit,
  testNodesLatency,
  probeNodesExit,
  listExits,
  getRuntime,
  resyncRuntime,
  getSettings,
  updateSettings
}

export default clashAPI
