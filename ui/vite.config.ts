import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'
import { writeFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

const outDir = fileURLToPath(new URL('../internal/webapp/dist', import.meta.url))

// Vite empties outDir before a build; the Go embed needs the directory to exist
// on a clean checkout, so the .gitkeep is restored after every build.
function keepGitkeep(): Plugin {
  return {
    name: 'ore-keep-gitkeep',
    closeBundle() {
      mkdirSync(outDir, { recursive: true })
      writeFileSync(join(outDir, '.gitkeep'), '')
    },
  }
}

// The dev loop: `npm run dev` serves the app at http://localhost:5173/app/ and
// proxies the API and git paths to a running `ore serve` (ORE_URL, default
// http://localhost:7777). The token cookie is set by POST /api/v0/webapp/session,
// which goes through the same proxy, so sign-in works in dev as in production.
const target = process.env.ORE_URL ?? 'http://localhost:7777'

export default defineConfig({
  base: '/app/',
  plugins: [react(), keepGitkeep()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': { target, changeOrigin: false },
      '/login': { target, changeOrigin: false },
      '/healthz': { target, changeOrigin: false },
    },
  },
  build: {
    outDir,
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2022',
    cssCodeSplit: true,
    chunkSizeWarningLimit: 400,
    rollupOptions: {
      output: {
        chunkFileNames(chunk) {
          const id = chunk.facadeModuleId ?? ''
          const lang = id.match(/node_modules\/@codemirror\/lang-([a-z0-9-]+)\//)
          if (lang) return `assets/lang-${lang[1]}-[hash].js`
          const mode = id.match(/legacy-modes\/mode\/([a-z0-9-]+)\.js/)
          if (mode) return `assets/mode-${mode[1]}-[hash].js`
          const lezer = id.match(/node_modules\/@lezer\/([a-z0-9-]+)\//)
          if (lezer) return `assets/lezer-${lezer[1]}-[hash].js`
          return 'assets/[name]-[hash].js'
        },
        manualChunks(id) {
          // Vite's preload helper is shared by every lazy chunk; keep it in the
          // always-loaded vendor chunk rather than letting Rollup attach it to
          // whichever heavy chunk happens to be emitted first.
          if (id.includes('vite/preload-helper') || id.includes('vite/modulepreload-polyfill')) return 'vendor'
          if (id.includes('node_modules')) {
            // Core editor packages share one chunk; language grammars stay in
            // their own lazy chunks (language-data imports them on demand).
            if (/@codemirror\/(state|view|language|merge|commands|language-data)\//.test(id) || /@lezer\/(common|highlight|lr)\//.test(id) || id.includes('style-mod') || id.includes('w3c-keyname') || id.includes('crelt')) return 'codemirror'
            if (id.includes('marked') || id.includes('dompurify')) return 'markdown'
            if (id.includes('react') || id.includes('@tanstack')) return 'vendor'
          }
          return undefined
        },
      },
    },
  },
})
