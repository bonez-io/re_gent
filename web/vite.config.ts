/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// https://vite.dev/config/
import path from 'node:path';
import { storybookTest } from '@storybook/addon-vitest/vitest-plugin';
import { playwright } from '@vitest/browser-playwright';

// More info at: https://storybook.js.org/docs/next/writing-tests/integrations/vitest-addon
export default defineConfig({
  plugins: [tailwindcss(), react()],
  server: {
    // Vite's default host binds only the IPv6 loopback ([::1]), so
    // http://127.0.0.1:5173 refuses to connect even though localhost:5173
    // works. Binding the literal IPv4 loopback here makes both work.
    host: '127.0.0.1',
    proxy: {
      '^/repos$': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7655', changeOrigin: true },
      // The skills registry is global, not repo-scoped, so it does not match the
      // '/<repo>/api' rule below. Without this the dev server answers with
      // index.html and the catalog silently falls back to the bundled list.
      '^/api/': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7655', changeOrigin: true },
      '^/healthz$': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7655', changeOrigin: true },
      '^/(?!src(?:/|$)|node_modules(?:/|$)|@|__)[a-z0-9][a-z0-9._-]*/api(?:/|$)': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7655', changeOrigin: true },
    },
  },
  test: {
    projects: [{
      extends: true,
      plugins: [
      // The plugin will run tests for the stories defined in your Storybook config
      // See options at: https://storybook.js.org/docs/next/writing-tests/integrations/vitest-addon#storybooktest
      storybookTest({
        configDir: path.join(import.meta.dirname, '.storybook')
      })],
      test: {
        name: 'storybook',
        browser: {
          enabled: true,
          headless: true,
          provider: playwright({}),
          instances: [{
            browser: 'chromium'
          }]
        }
      }
    }]
  }
});
