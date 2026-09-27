import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyTheme, themeOriginFromEvent } from '../themeTransition'

const root = document.documentElement

let rafQueue: FrameRequestCallback[] = []
let idleQueue: IdleRequestCallback[] = []

function flushFrames() {
  // 先跑一帧 rAF，再跑空闲回调（与浏览器中的先后顺序一致）
  const frames = rafQueue
  rafQueue = []
  frames.forEach((cb) => cb(performance.now()))
  const idle = idleQueue
  idleQueue = []
  idle.forEach((cb) => cb({ didTimeout: false, timeRemaining: () => 50 } as IdleDeadline))
}

function mockMatchMedia(reduceMotion: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query.includes('prefers-reduced-motion') ? reduceMotion : false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {}
  }))
}

describe('applyTheme', () => {
  beforeEach(() => {
    root.className = ''
    localStorage.clear()
    rafQueue = []
    idleQueue = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      rafQueue.push(cb)
      return rafQueue.length
    })
    vi.stubGlobal('requestIdleCallback', (cb: IdleRequestCallback) => {
      idleQueue.push(cb)
      return idleQueue.length
    })
    mockMatchMedia(false)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    delete (document as { startViewTransition?: unknown }).startViewTransition
  })

  it('switches theme, persists it and suspends transitions only around the switch', () => {
    applyTheme(true)

    expect(root.classList.contains('dark')).toBe(true)
    expect(localStorage.getItem('theme')).toBe('dark')
    expect(root.classList.contains('theme-switching')).toBe(true)

    flushFrames()
    expect(root.classList.contains('theme-switching')).toBe(false)
  })

  it('does not suspend transitions when the theme is unchanged', () => {
    root.classList.add('dark')
    applyTheme(true)

    expect(root.classList.contains('theme-switching')).toBe(false)
    expect(localStorage.getItem('theme')).toBe('dark')
  })

  it('lets only the latest switch restore transitions', () => {
    applyTheme(true)
    applyTheme(false)
    const pendingFromFirst = rafQueue.shift()!
    pendingFromFirst(performance.now())
    idleQueue.shift()!({ didTimeout: false, timeRemaining: () => 50 } as IdleDeadline)

    expect(root.classList.contains('theme-switching')).toBe(true)
    flushFrames()
    expect(root.classList.contains('theme-switching')).toBe(false)
    expect(root.classList.contains('dark')).toBe(false)
  })

  it('reveals the new theme from the given origin with a view transition', async () => {
    let resolveFinished!: () => void
    const finished = new Promise<void>((resolve) => {
      resolveFinished = resolve
    })
    const startViewTransition = vi.fn((update: () => void) => {
      update()
      return { ready: Promise.resolve(), finished }
    })
    ;(document as { startViewTransition?: unknown }).startViewTransition = startViewTransition
    const animate = vi.fn()
    root.animate = animate as unknown as typeof root.animate

    applyTheme(true, { x: 10, y: 20 })
    expect(startViewTransition).toHaveBeenCalledTimes(1)
    expect(root.classList.contains('dark')).toBe(true)

    await Promise.resolve()
    expect(animate).toHaveBeenCalledTimes(1)
    const [keyframes, options] = animate.mock.calls[0]
    expect(keyframes.clipPath[0]).toBe('circle(0px at 10px 20px)')
    expect(options.pseudoElement).toBe('::view-transition-new(root)')

    expect(root.classList.contains('theme-switching')).toBe(true)
    resolveFinished()
    await finished
    await Promise.resolve()
    await Promise.resolve()
    flushFrames()
    expect(root.classList.contains('theme-switching')).toBe(false)
  })

  it('skips the view transition when reduced motion is preferred', () => {
    mockMatchMedia(true)
    const startViewTransition = vi.fn()
    ;(document as { startViewTransition?: unknown }).startViewTransition = startViewTransition

    applyTheme(true)

    expect(startViewTransition).not.toHaveBeenCalled()
    expect(root.classList.contains('dark')).toBe(true)
  })
})

describe('themeOriginFromEvent', () => {
  it('uses the pointer position for mouse clicks', () => {
    const event = new MouseEvent('click', { clientX: 30, clientY: 40, detail: 1 })
    expect(themeOriginFromEvent(event)).toEqual({ x: 30, y: 40 })
  })

  it('falls back to the button center for keyboard activation', () => {
    const button = document.createElement('button')
    button.getBoundingClientRect = () => ({ left: 100, top: 200, width: 40, height: 20 }) as DOMRect
    const event = new MouseEvent('click', { detail: 0 })
    Object.defineProperty(event, 'currentTarget', { value: button })

    expect(themeOriginFromEvent(event)).toEqual({ x: 120, y: 210 })
  })
})
