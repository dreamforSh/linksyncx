import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { compileStyle } from 'vue/compiler-sfc'
import { describe, expect, it } from 'vitest'

// In <style scoped>, Vue replaces a whole selector that contains `:global(X)` with just `X`:
// `:global(.dark) .skeleton` compiles to a bare `.dark {}` rule, so the dark style never reaches
// the element and the declarations land on <html class="dark"> instead. Write `.dark .skeleton`
// (compiles to `.dark .skeleton[data-v-xxx]`) or `:global(.dark .skeleton)` for elements outside
// the component.

const srcDir = resolve(dirname(fileURLToPath(import.meta.url)), '..')

const GLOBAL_PSEUDO = String.raw`(?::global|::v-global)\([^()]*\)`
// `:global(X)` followed by more selector: `:global(.dark) .x`, `:global(.dark):hover`
const GLOBAL_WITH_TAIL = new RegExp(GLOBAL_PSEUDO + String.raw`\s*[^\s{,]`, 'g')
// more selector before `:global(X)`: `.x :global(.dark)`
const GLOBAL_WITH_HEAD = new RegExp(String.raw`[^\s{},;]\s*` + GLOBAL_PSEUDO, 'g')

function blankComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, ' '))
}

function findBrokenGlobalSelectors(css: string): number[] {
  const code = blankComments(css)
  return [...code.matchAll(GLOBAL_WITH_TAIL), ...code.matchAll(GLOBAL_WITH_HEAD)].map(
    (match) => match.index ?? 0
  )
}

function listVueFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return listVueFiles(path)
    return entry.name.endsWith('.vue') ? [path] : []
  })
}

describe('scoped style :global() selectors', () => {
  it.each([
    [':global(.dark) .x { color: red }', true],
    ['.x :global(.dark) { color: red }', true],
    [':global(.dark):hover { color: red }', true],
    ['.dark .x { color: red }', false],
    [':global(.dark .x) { color: red }', false],
    [':global(.a), .b { color: red }', false],
    ['/* was :global(.dark) .x */ .dark .x { color: red }', false]
  ])('detector flags %j = %s, same as the compiler collapsing it to a bare .dark rule', (css, broken) => {
    expect(findBrokenGlobalSelectors(css).length > 0).toBe(broken)
    const { code } = compileStyle({ source: css, filename: 'probe.vue', id: 'data-v-probe', scoped: true })
    expect(/(^|\})\s*\.dark\s*\{/.test(code)).toBe(broken)
  })

  it('no .vue style block combines :global() with other selector parts', () => {
    const offenders: string[] = []
    for (const file of listVueFiles(srcDir)) {
      const source = readFileSync(file, 'utf8')
      for (const block of source.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/g)) {
        const contentStart = (block.index ?? 0) + block[0].indexOf('>') + 1
        for (const offset of findBrokenGlobalSelectors(block[1])) {
          const at = contentStart + offset
          const line = source.slice(0, at).split('\n').length
          const text = source.slice(source.lastIndexOf('\n', at) + 1, source.indexOf('\n', at)).trim()
          offenders.push(`${relative(srcDir, file).replace(/\\/g, '/')}:${line}: ${text}`)
        }
      }
    }
    expect(offenders).toEqual([])
  })
})
