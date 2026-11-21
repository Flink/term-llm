import { resolve } from 'node:path';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  base: './',
  publicDir: false,
  plugins: [
    {
      name: 'standalone-proxy',
      writeBundle(_options, bundle) {
        const files = Object.keys(bundle).sort();
        if (files.join(',') !== 'proxy.css,proxy.js') {
          throw new Error(`Proxy must emit only proxy.js and proxy.css, got ${files.join(', ')}`);
        }
      },
    },
  ],
  build: {
    outDir: resolve(import.meta.dirname, '../internal/serveui/static/dist'),
    emptyOutDir: false,
    manifest: false,
    sourcemap: false,
    minify: 'esbuild',
    cssCodeSplit: false,
    rollupOptions: {
      input: resolve(import.meta.dirname, 'proxy/main.ts'),
      output: {
        entryFileNames: 'proxy.js',
        assetFileNames: 'proxy[extname]',
        codeSplitting: false,
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['proxy/**/*.test.ts'],
    testTimeout: 20_000,
    // Assertions expect en-US number/date formatting; run workers in that
    // locale whatever the host's (Node reads it from the environment at start).
    env: { LC_ALL: 'en_US.UTF-8' },
  },
});
