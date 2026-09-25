import { readonly, ref } from 'vue'

// 主题切换只改 <html> 上的 dark class（见 AppSidebar.toggleTheme），没有集中的 store。
// 这里用一个全局共享的 MutationObserver 把它变成响应式状态，图表等依赖配色的组件
// 在切换主题时能立即重算颜色，而不是等到重新挂载。
const isDark = ref(false)
let observer: MutationObserver | null = null

function readDarkClass(): boolean {
  return typeof document !== 'undefined' && document.documentElement.classList.contains('dark')
}

function ensureObserver() {
  if (observer || typeof MutationObserver === 'undefined' || typeof document === 'undefined') return
  isDark.value = readDarkClass()
  observer = new MutationObserver(() => {
    const next = readDarkClass()
    if (next !== isDark.value) isDark.value = next
  })
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
}

export function useDarkMode() {
  ensureObserver()
  // 观察器不可用（如部分测试环境）时，至少返回当前快照
  if (!observer) isDark.value = readDarkClass()
  return readonly(isDark)
}
