/**
 * 主题切换：
 * 1. 切换期间给 <html> 加 .theme-switching，暂时禁用全站 CSS transition。
 *    否则数百个带 transition-colors / transition-all 的元素会同时起颜色过渡，
 *    主线程被样式重算拖住半秒以上（实测 4x 降速下单个长任务 500ms+）。
 * 2. 浏览器支持 View Transitions 且未开启「减少动态效果」时，新主题从点击位置圆形扩散。
 */

export interface ThemeTransitionOrigin {
  x: number
  y: number
}

type ViewTransitionLike = {
  ready: Promise<void>
  finished: Promise<void>
}

type DocumentWithViewTransition = Document & {
  startViewTransition?: (update: () => void) => ViewTransitionLike
}

const SWITCHING_CLASS = 'theme-switching'
const REVEAL_DURATION_MS = 400

// 连续快速切换时，只由最后一次切换负责恢复 transition
let switchSeq = 0

function commitTheme(dark: boolean) {
  document.documentElement.classList.toggle('dark', dark)
  try {
    localStorage.setItem('theme', dark ? 'dark' : 'light')
  } catch {
    // 隐私模式等场景下写入失败不影响切换
  }
}

// 在应用了新主题的那一帧渲染之后再恢复 transition：rAF 回调先于当帧样式计算执行，
// 之后放到空闲时段移除（移除同样会触发一次全量样式重算，避开用户交互）
function releaseTransitions(seq: number) {
  const release = () => {
    if (seq === switchSeq) document.documentElement.classList.remove(SWITCHING_CLASS)
  }
  requestAnimationFrame(() => {
    if (typeof window.requestIdleCallback === 'function') {
      window.requestIdleCallback(release, { timeout: 1000 })
    } else {
      setTimeout(release, 50)
    }
  })
}

/** 从触发事件推算扩散圆心；键盘触发（detail 为 0）时取按钮中心 */
export function themeOriginFromEvent(event?: MouseEvent): ThemeTransitionOrigin | undefined {
  if (!event) return undefined
  if (event.detail > 0 && (event.clientX || event.clientY)) {
    return { x: event.clientX, y: event.clientY }
  }
  const target = event.currentTarget as HTMLElement | null
  if (!target?.getBoundingClientRect) return undefined
  const rect = target.getBoundingClientRect()
  return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }
}

export function applyTheme(dark: boolean, origin?: ThemeTransitionOrigin) {
  const root = document.documentElement
  if (root.classList.contains('dark') === dark) {
    commitTheme(dark)
    return
  }

  const seq = ++switchSeq
  root.classList.add(SWITCHING_CLASS)

  const doc = document as DocumentWithViewTransition
  const reduceMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  if (typeof doc.startViewTransition !== 'function' || reduceMotion) {
    commitTheme(dark)
    releaseTransitions(seq)
    return
  }

  const transition = doc.startViewTransition(() => commitTheme(dark))
  transition.ready
    .then(() => {
      const x = origin?.x ?? window.innerWidth / 2
      const y = origin?.y ?? window.innerHeight / 2
      const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y))
      root.animate(
        { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`] },
        {
          duration: REVEAL_DURATION_MS,
          easing: 'cubic-bezier(0.4, 0, 0.2, 1)',
          pseudoElement: '::view-transition-new(root)'
        }
      )
    })
    .catch(() => {
      // 过渡被跳过（如页面隐藏）时主题已生效，无需处理
    })
  transition.finished
    .catch(() => {})
    .finally(() => releaseTransitions(seq))
}
