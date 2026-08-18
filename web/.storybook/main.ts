import type { StorybookConfig } from '@storybook/react-vite';

const config: StorybookConfig = {
  staticDirs: ['../public'],
  "stories": ["../src/**/*.stories.@(js|jsx|mjs|ts|tsx)"],
  "addons": [
    "@chromatic-com/storybook",
    "@storybook/addon-vitest",
    "@storybook/addon-a11y",
    "@storybook/addon-docs",
    "@storybook/addon-mcp",
    "msw-storybook-addon"
  ],
  "framework": "@storybook/react-vite"
};
export default config;
