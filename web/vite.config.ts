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
    proxy: {
      '^/repos$': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7654', changeOrigin: true },
      '^/healthz$': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7654', changeOrigin: true },
      '^/(?!src(?:/|$)|node_modules(?:/|$)|@|__)[a-z0-9][a-z0-9._-]*/api(?:/|$)': { target: process.env.VITE_REGENT_SERVER_URL || 'http://127.0.0.1:7654', changeOrigin: true },
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
