// 用户头像占位：首字母 + 按用户 ID 稳定取色的浅色底。仅作装饰性区分，不承载数据含义。
const AVATAR_TONES = [
  'bg-teal-100 text-teal-800 dark:bg-teal-500/15 dark:text-teal-200',
  'bg-orange-100 text-orange-800 dark:bg-orange-500/15 dark:text-orange-200',
  'bg-blue-100 text-blue-800 dark:bg-blue-500/15 dark:text-blue-200',
  'bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-200',
  'bg-pink-100 text-pink-800 dark:bg-pink-500/15 dark:text-pink-200',
  'bg-green-100 text-green-800 dark:bg-green-500/15 dark:text-green-200',
  'bg-violet-100 text-violet-800 dark:bg-violet-500/15 dark:text-violet-200',
  'bg-rose-100 text-rose-800 dark:bg-rose-500/15 dark:text-rose-200'
] as const

export function avatarToneClass(seed: number | string): string {
  const key = typeof seed === 'number' ? seed : Array.from(seed).reduce((acc, ch) => acc + ch.charCodeAt(0), 0)
  const index = Math.abs(Math.trunc(key)) % AVATAR_TONES.length
  return AVATAR_TONES[index]
}

/** alice.chen@acme.io → AC；wanglei → WA；张三 → 张 */
export function userInitials(name: string | null | undefined): string {
  const source = (name ?? '').trim()
  if (!source) return '?'
  const first = Array.from(source)[0]
  if (first.charCodeAt(0) > 127) return first
  const local = source.split('@')[0]
  const parts = local.split(/[._\-\s]+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return local.slice(0, 2).toUpperCase()
}
