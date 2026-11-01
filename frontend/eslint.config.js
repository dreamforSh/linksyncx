import js from '@eslint/js'
import tsPlugin from '@typescript-eslint/eslint-plugin'
import tsParser from '@typescript-eslint/parser'
import vue from 'eslint-plugin-vue'
import globals from 'globals'
import vueParser from 'vue-eslint-parser'

export default [
  {
    ignores: ['**/node_modules/**', 'dist/**', '.cache/**', '.vite/**', 'vite.config.js', 'vite.config.d.ts'],
  },
  js.configs.recommended,
  ...vue.configs['flat/essential'],
  ...tsPlugin.configs['flat/recommended'],
  {
    files: ['**/*.{vue,js,jsx,cjs,mjs,ts,tsx,cts,mts}'],
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
      ecmaVersion: 'latest',
      sourceType: 'module',
      parserOptions: {
        parser: tsParser,
        extraFileExtensions: ['.vue'],
      },
    },
    rules: {
      'no-constant-condition': 'off',
      'no-useless-assignment': 'off',
      'no-useless-escape': 'off',
      'no-unused-vars': 'off',
      '@typescript-eslint/no-unused-expressions': ['error', { allowTernary: true }],
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrors: 'none' }],
      '@typescript-eslint/no-empty-object-type': 'off',
      '@typescript-eslint/no-unsafe-function-type': 'off',
      '@typescript-eslint/no-wrapper-object-types': 'off',
      '@typescript-eslint/ban-ts-comment': 'off',
      '@typescript-eslint/no-explicit-any': 'off',
      'vue/multi-word-component-names': 'off',
      'vue/no-use-v-if-with-v-for': 'off',
    },
  },
  {
    files: ['**/*.vue'],
    languageOptions: { parser: vueParser },
  },
]
