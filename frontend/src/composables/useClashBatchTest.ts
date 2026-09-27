/**
 * Runs Clash node latency tests / exit probes over many nodes in small chunks,
 * so rows can update while the run progresses and the admin can stop it.
 */

import { computed, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import type { ClashExitProbeResult, ClashLatencyResult } from '@/types'

/** latency: delay test only; exit: egress + platform probe; full: probe the nodes whose delay test passed. */
export type ClashBatchKind = 'latency' | 'exit' | 'full'

export interface ClashBatchProgress {
  kind: ClashBatchKind
  total: number
  /** Nodes whose chunk finished (tested or skipped). */
  done: number
  success: number
  failed: number
  /** Exit probes that found a new egress IP awaiting confirmation. */
  changed: number
  /** Nodes the server did not test (disabled, missing, subscription off). */
  skipped: number
  running: boolean
  cancelled: boolean
}

export interface ClashBatchCallbacks {
  onLatency?: (results: ClashLatencyResult[]) => void
  onExit?: (results: ClashExitProbeResult[]) => void
}

// Delay tests are cheap and run through the core; exit probes query an IP
// lookup service through each node, which rate-limits aggressive callers.
const CHUNK: Record<ClashBatchKind, number> = { latency: 10, exit: 5, full: 5 }
const CONCURRENCY: Record<ClashBatchKind, number> = { latency: 3, exit: 2, full: 2 }

export function useClashBatchTest(callbacks: ClashBatchCallbacks = {}) {
  const progress = ref<ClashBatchProgress | null>(null)
  const running = computed(() => progress.value?.running === true)
  let stopRequested = false

  async function runChunk(state: ClashBatchProgress, ids: number[]) {
    let probeIds = ids
    try {
      if (state.kind !== 'exit') {
        const results = await adminAPI.clash.testNodesLatency({ node_ids: ids })
        callbacks.onLatency?.(results)
        state.skipped += ids.length - results.length
        const passed = results.filter((result) => result.success)
        state.failed += results.length - passed.length
        if (state.kind === 'latency') {
          state.success += passed.length
          return
        }
        probeIds = passed.map((result) => result.node_id)
      }
      if (probeIds.length === 0) return
      const results = await adminAPI.clash.probeNodesExit({ node_ids: probeIds })
      callbacks.onExit?.(results)
      if (state.kind === 'exit') state.skipped += probeIds.length - results.length
      for (const result of results) {
        if (result.success) state.success++
        else state.failed++
        if (result.exit_status === 'changed') state.changed++
      }
      // In a full run every node that passed the delay test must be accounted for.
      if (state.kind === 'full') state.failed += probeIds.length - results.length
    } catch {
      // The whole chunk failed (network, core down); count what was not yet settled.
      state.failed += probeIds.length
    } finally {
      state.done += ids.length
    }
  }

  /** Resolves with the final counts; a second call while running is ignored (null). */
  async function run(kind: ClashBatchKind, ids: number[]): Promise<ClashBatchProgress | null> {
    if (running.value) return null
    stopRequested = false
    progress.value = {
      kind,
      total: ids.length,
      done: 0,
      success: 0,
      failed: 0,
      changed: 0,
      skipped: 0,
      running: true,
      cancelled: false
    }
    const state = progress.value
    const size = CHUNK[kind]
    const chunks: number[][] = []
    for (let start = 0; start < ids.length; start += size) {
      chunks.push(ids.slice(start, start + size))
    }
    let next = 0
    const worker = async () => {
      while (!stopRequested && next < chunks.length) {
        const chunk = chunks[next++]
        await runChunk(state, chunk)
      }
    }
    await Promise.all(Array.from({ length: Math.min(CONCURRENCY[kind], chunks.length) }, () => worker()))
    state.cancelled = stopRequested && state.done < state.total
    state.running = false
    return { ...state }
  }

  /** Lets in-flight chunks finish and starts no new ones. */
  function stop() {
    if (running.value) stopRequested = true
  }

  function dismiss() {
    if (!running.value) progress.value = null
  }

  return { progress, running, run, stop, dismiss }
}
