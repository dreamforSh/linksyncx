import type { InjectionKey } from 'vue'

/**
 * App.vue 顶层常驻一个 AppShell，页面里的 <AppLayout> 通过它登记，
 * 侧边栏/顶栏因此在路由切换时保持挂载，不再随页面销毁重建。
 */
export interface AppShellContext {
  register: () => void
  unregister: () => void
}

export const APP_SHELL_KEY: InjectionKey<AppShellContext> = Symbol('app-shell')

// 与侧栏宽度 w-64 / w-[72px] 保持一致
export const SIDEBAR_EXPANDED_WIDTH = 256
export const SIDEBAR_COLLAPSED_WIDTH = 72
// 与 .sidebar 的宽度过渡保持一致，保证侧栏与内容区同步移动
export const SIDEBAR_TRANSITION_MS = 250
export const SIDEBAR_TRANSITION_EASING = 'cubic-bezier(0.2, 0, 0, 1)'
