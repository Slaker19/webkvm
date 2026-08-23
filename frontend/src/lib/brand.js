/* global __APP_VERSION__ */
/**
 * brand.js — single source of truth for user-facing brand strings.
 *
 * The site name is the title rendered in the sidebar, login screen, and
 * <title> tag. For now it is a static constant; the next phase will pull
 * it from `GET /api/ui/branding` (Phase 1.7-bis Lote 2) and the value
 * below will become a fallback used until that endpoint resolves.
 *
 * Version is injected at build time by vite.config.js (`define:
 * __APP_VERSION__`) so the bundle is self-contained.
 */
export const SITE_NAME = 'WebKVM';
export const SITE_TAGLINE = 'Sign in to manage virtual machines';

export const APP_VERSION = __APP_VERSION__ || 'dev';
