import './app.css';
import { mount } from 'svelte';
import App from './App.svelte';

// Apply the `.dark` class to the document root so all Tailwind
// `dark:` utilities from the shadcn primitives (button outline,
// tooltip, dialog, etc.) resolve correctly. The base `:root`
// tokens in `app.css` are already dark, so this is purely about
// activating the `dark:` variant — the visual change is a no-op.
if (typeof document !== 'undefined') {
  document.documentElement.classList.add('dark');
}

const app = mount(App, {
  target: document.getElementById('app'),
});

export default app;
