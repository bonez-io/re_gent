import type { Preview } from '@storybook/react-vite'
import { mswLoader } from 'msw-storybook-addon/csf3'
import '../src/index.css'
import { mswHandlers } from './msw-handlers'

const preview: Preview = {
  decorators: [
    (Story) => <div className="min-h-screen bg-page p-8 text-ink"><Story /></div>,
  ],
  loaders: [mswLoader()],
  async beforeEach({ msw }) {
    msw.use(...mswHandlers)
  },
  parameters: {
    controls: {
      matchers: {
       color: /(background|color)$/i,
       date: /Date$/i,
      },
    },

    a11y: {
      // 'todo' - show a11y violations in the test UI only
      // 'error' - fail CI on a11y violations
      // 'off' - skip a11y checks entirely
      test: 'error'
    }
  },
};

export default preview;
