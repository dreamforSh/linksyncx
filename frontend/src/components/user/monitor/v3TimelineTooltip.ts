import type { InjectionKey } from 'vue'

export type V3TooltipAlign = 'start' | 'center' | 'end'

/**
 * Shared hover/focus tooltip state for every V3 timeline on the page. Timelines write it
 * from delegated events; only ChannelMonitorV3Tooltip reads it, so hovering re-renders
 * that one tooltip instead of the board, the card or the timeline.
 */
export interface V3TooltipState {
  visible: boolean
  left: number
  top: number
  align: V3TooltipAlign
  text: string
  /** Timeline that opened the tooltip, so a leave from another timeline cannot close it. */
  owner: symbol | null
}

export interface V3TooltipHost {
  id: string
  state: V3TooltipState
}

/** Provided by the page so all timelines share one tooltip instead of one Teleport each. */
export const V3_TOOLTIP_HOST: InjectionKey<V3TooltipHost> = Symbol('channel-monitor-v3-tooltip')

export function createV3TooltipState(): V3TooltipState {
  return { visible: false, left: 0, top: 0, align: 'center', text: '', owner: null }
}

const VIEWPORT_GUTTER = 16
const MAX_TOOLTIP_WIDTH = 280

/** Anchors the tooltip above `rect`, flipping its alignment near the viewport edges. */
export function showV3Tooltip(state: V3TooltipState, owner: symbol, rect: DOMRect, text: string) {
  const viewportWidth = typeof window === 'undefined' ? 0 : window.innerWidth
  const maxWidth = Math.min(MAX_TOOLTIP_WIDTH, viewportWidth - VIEWPORT_GUTTER * 2)
  const center = rect.left + rect.width / 2
  if (center + maxWidth / 2 > viewportWidth - VIEWPORT_GUTTER) {
    state.left = viewportWidth - VIEWPORT_GUTTER
    state.align = 'end'
  } else if (center - maxWidth / 2 < VIEWPORT_GUTTER) {
    state.left = VIEWPORT_GUTTER
    state.align = 'start'
  } else {
    state.left = center
    state.align = 'center'
  }
  state.top = rect.top - 8
  state.text = text
  state.owner = owner
  state.visible = Boolean(text)
}

export function hideV3Tooltip(state: V3TooltipState, owner: symbol) {
  if (state.owner !== owner) return
  state.visible = false
  state.owner = null
}
