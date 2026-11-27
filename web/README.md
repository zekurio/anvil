# anvil web UI

The browser UI for `anvild`. SolidJS 2, TypeScript, Vite, and UI SDK
primitives under the anvil design system. `npm run build` writes `web/dist`,
which the daemon embeds (`embed.go`) and serves next to the API.

## Commands

Node 22.12 or newer. On a Nix machine: `nix shell nixpkgs#nodejs -c npm <command>`.

| Command | What it does |
| --- | --- |
| `npm ci` | Install the pinned dependencies. |
| `npm run dev:mock` | Dev server on <http://localhost:5173> with an in-memory fake API. No daemon needed. |
| `npm run dev` | Dev server that proxies `/api` to a running daemon at `http://127.0.0.1:8484`. Set `ANVIL_API` to point elsewhere. |
| `npm run typecheck` | `tsc --noEmit`. |
| `npm run build` | Production bundle in `dist/`. `dist/.gitkeep` is kept. |
| `npm run preview` | Serve the built bundle. |
| `npm run icons` | Regenerate `src/ui/icon-data.ts` after editing the icon list in `scripts/vendor-icons.mjs`. |

`VITE_MOCK_AUTH=1 npm run dev:mock` turns on the sign-in screen; the password
is `anvil`. `VITE_MOCK_LISTEN=0.0.0.0:8484 npm run dev:mock` makes the mock
report an open listen address, which the Settings page warns about. The mock
keeps its state in memory and resets when the dev server restarts.
`POST /api/v1/_mock/jobs/{id}/step` with `{"step": "publish"}` is a mock-only
route that moves a running job to the start of a step, for looking at the
short ones.

`/_dev/components` shows every component in both themes. It exists in dev
builds only.

To see the flow editor handle a save conflict, open a flow, then run
`curl -X POST localhost:5173/api/v1/flows/<id>/_touch`: the mock bumps the
flow's revision as if someone else had saved it, and the next save answers 409.
A second tab that saves the same flow does the same.

## Layout

```
src/
  api/        types.ts mirrors the Go contract 1:1; client, endpoints, query keys, createQuery
  lib/        format, theme, document title, mutation helper, unsaved-changes guard
  ui/         the design system: components, styles/, fonts/, vendored icons
  shell/      app frame: sidebar, connection banner, sign-in, shared polled data
  features/   what several pages share: jobs/ (job sheet, bulk bar, the words for steps and reasons),
              libraries/ (scan, the PUT body), layout/ (pager, form sections, row selection, form bar)
  pages/      one directory per route; index.tsx default-exports the page
  routes.tsx  the route table
mock/         dev-only fake API (Vite plugin); never part of a build
scripts/      vendor-icons.mjs
```

## Conventions

- **Route.** Add a `pages/<name>/index.tsx` with a default export and one
  entry in `src/routes.tsx`. Pages render their own `<Topbar>` and call
  `useTitle`.
- **Read.** `createQuery(() => keys.x(...), () => api.x(...), { poll })`,
  read under the page's `<Loading>`/`<Errored>` (the shell provides both).
  Session, status, libraries and flows are already polled: `useAppData()`.
  For a query whose key changes with a filter or a page, pass
  `deferred: true` and show `isPending(() => data())`.
- **Write.** `run(() => api.x(...), { invalidate: [keys.x], success, failure, onError })`
  from `lib/mutate`. It refetches, then toasts.
- **Feedback.** `toast.success(...)`, `await confirm({ ... })`,
  `<Dialog open onClose>`, all from `ui`.
- **Forms over polled data.** `createDraft(() => saved)` from `lib/draft`: the
  form follows the server until the first edit, and no poll resets a draft.
  Pair it with `<FormBar>` and `useUnsavedChanges(draft.dirty)`.
- **Paged tables.** One page of rows is loaded at a time (`<Pager>`), with
  `createSelection` keeping a selection across pages and polls.
- **API types.** Change `src/api/types.ts` only together with the Go type it
  mirrors; every block names its source.
- **Mock.** Routes live in `mock/handlers/*.ts`, state in `mock/db.ts`, seed
  data in `mock/fixtures/`. Relative imports there need the `.ts` extension
  because Vite's config loader runs them unbundled.
- **Styles.** Plain CSS with the `av-` prefix and the tokens in
  `ui/styles/tokens.css`. No raw colours or sizes in pages.

## Third-party assets

- IBM Plex Sans and IBM Plex Mono (`src/ui/fonts/`), SIL Open Font License
  1.1; the licence texts sit next to the files.
- Icons from Lucide, vendored as inline SVG (`src/ui/icon-data.ts`), ISC
  License; see `src/ui/icon-data.LICENSE`. `lucide-solid` does not support
  Solid 2 yet.
