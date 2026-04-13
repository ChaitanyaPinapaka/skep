// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://skep.sh',
  integrations: [
    starlight({
      title: 'Skep',
      description: 'AI agents for every repo. They talk to each other.',
      logo: {
        light: './public/logo-light.svg',
        dark: './public/logo-dark.svg',
        replacesTitle: true,
      },
      components: {
        Hero: './src/components/Hero.astro',
      },
      favicon: '/favicon.svg',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/ChaitanyaPinapaka/skep' },
      ],
      editLink: {
        baseUrl: 'https://github.com/ChaitanyaPinapaka/skep/edit/main/site/',
      },
      customCss: ['./src/styles/skep-theme.css'],
      sidebar: [
        {
          label: 'Getting started',
          items: [
            { label: 'Install', link: '/getting-started/install/' },
            { label: 'Quickstart', link: '/getting-started/quickstart/' },
            { label: 'The cockpit', link: '/getting-started/cockpit/' },
          ],
        },
        {
          label: 'Concepts',
          items: [
            { label: 'Architecture', link: '/concepts/architecture/' },
            { label: 'Workspaces & agents', link: '/concepts/workspace/' },
            { label: 'Task lifecycle', link: '/concepts/task-lifecycle/' },
            { label: 'Cross-repo work', link: '/concepts/cross-repo/' },
            { label: 'MCP integration', link: '/concepts/mcp/' },
          ],
        },
        {
          label: 'Guides',
          items: [
            { label: 'Add a health endpoint', link: '/guides/health-check/' },
            { label: 'Cross-repo auth rewrite', link: '/guides/cross-repo-auth/' },
            { label: 'Refactor with MCP tools', link: '/guides/refactor-with-mcp/' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'CLI commands', link: '/reference/commands/' },
            { label: 'Configuration', link: '/reference/config/' },
            { label: 'MCP tools', link: '/reference/mcp-tools/' },
          ],
        },
        {
          label: 'Integrations',
          items: [
            { label: 'Claude Code', link: '/integrations/claude-code/' },
            { label: 'VS Code', link: '/integrations/vscode/' },
            { label: 'JetBrains', link: '/integrations/jetbrains/' },
          ],
        },
        {
          label: 'Advanced',
          items: [
            { label: 'Benchmarking', link: '/advanced/benchmarking/' },
            { label: 'Troubleshooting', link: '/advanced/troubleshooting/' },
          ],
        },
        { label: 'FAQ', link: '/faq/' },
        { label: 'Contributing', link: '/contributing/' },
        { label: 'Changelog', link: '/changelog/' },
        { label: 'Why "skep"?', link: '/about/' },
      ],
    }),
  ],
});
