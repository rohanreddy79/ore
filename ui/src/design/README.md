# Ore design system

The design system lives in `ui/src/design/`: one token sheet, a base sheet, a component sheet and a small React component library. Everything in the app reads tokens; nothing hard-codes a colour, size or duration. `/app/design` renders a living gallery of all of it.

## Decisions

**Aesthetic.** A forge: warm neutrals (paper in light mode, graphite in dark), one copper accent, and a restrained palette of semantic colours. The warmth sets it apart from the blue-grey of every other forge while keeping the density and rhythm GitHub users expect: 13–14 px body text, 4-point spacing, sticky headers, pills for state.

**One token set, two themes.** `tokens.css` defines every variable on `:root` for light, redefines them under `@media (prefers-color-scheme: dark) :root:not([data-theme='light'])` and again under `:root[data-theme='dark']`. The stored preference (`localStorage ore.theme`) is applied before paint from `index.html`, so there is no flash. The theme menu offers light, dark and follow-system.

**System fonts.** `--font-sans` and `--font-mono` are system stacks. No font download, no layout shift, no third-party request; the shell stays well under its size budget.

**Contrast.** `--fg` and `--fg-muted` meet WCAG AA (≥ 4.5:1) on `--bg` and `--surface` in both themes; `--fg-subtle` is decorative only (≥ 3:1). Status pills pair a soft background with a darker foreground of the same hue (`--ok-soft` / `--ok-fg`).

**Focus.** `:focus-visible` draws `--focus-ring` (a two-tone ring that works on any surface) on every control; menus, dialogs and the palette are keyboard-operable.

**Motion.** Three durations (120/180/260 ms) and one easing curve; all collapse to 0 ms under `prefers-reduced-motion`.

## Tokens

| Group | Variables |
|---|---|
| Surfaces | `--bg --surface --surface-2 --surface-3 --surface-raised --overlay --border --border-strong --border-focus` |
| Text | `--fg --fg-muted --fg-subtle --fg-on-accent --link` |
| Accent | `--accent --accent-hover --accent-soft --accent-fg` |
| Semantic | `--ok --danger --warn --info --agent --bot`, each with `-soft` and `-fg` |
| Diff | `--diff-add-bg --diff-add-gutter --diff-add-word --diff-del-* --diff-hunk-bg --diff-hunk-fg --diff-gutter-fg` |
| Code | `--code-keyword --code-string --code-number --code-comment --code-def --code-type --code-prop --code-op --code-meta --code-invalid` |
| Type | `--font-sans --font-mono`, scale `--fs-0 … --fs-7` (11/12/13/14/16/20/24/32), `--lh`, `--lh-tight`, weights |
| Space | `--sp-1 … --sp-8` (4/8/12/16/24/32/48/64) |
| Radius | `--radius-1 --radius-2 --radius-3 --radius-full` (4/6/10/999) |
| Elevation | `--shadow-1 --shadow-2 --shadow-3` |
| Motion | `--motion-fast --motion --motion-slow --ease` |
| Layout | `--topbar-h --content-max --sidebar-w --gutter` |

Agents get their own hue (`--agent`, violet): agent actors, agent lanes, agent-authored provenance badges and the L4/L5 level badges all use it, so "an agent did this" is one glance away everywhere.

## Components (`src/design/index.ts`)

| Component | Use | Notes |
|---|---|---|
| `Button` | every action | variants `primary` (one per view), `default`, `danger`, `ghost`; sizes `sm` `md` `lg`; `icon` needs `aria-label`; `loading` shows a spinner and disables |
| `Input` `Textarea` `Select` `Checkbox` `Field` | forms | `Field` wires label, hint and error (`role=alert`); `aria-invalid` turns the border red |
| `Kbd` | shortcuts | in menus, the palette footer and the help dialog |
| `Pill` `StatusPill` | state | `statusTone()` maps every API status string to a tone; live states pulse |
| `Avatar` `ActorChip` | actors | kind-aware: humans get initials in a circle, agents a cpu glyph on a violet square, bots a grey square |
| `Card` `Stat` | grouping | `tight` removes padding for lists and tables inside |
| `Tabs` | navigation | links with `aria-current=page`; vertical variant for settings |
| `Table` | tabular data | sticky header, `hideSm` columns at phone width; use `.list` rows for navigable lists |
| `Menu` | dropdowns | `role=menu`, arrow keys, Escape, click outside |
| `Dialog` | modals | native `<dialog>`: focus trap, Escape, backdrop; `wide` variant for editors |
| `ToastProvider` `useToast` `toast()` | feedback | bottom-right, `aria-live`; the API client raises 5xx toasts with a Retry action |
| `EmptyState` | zero data | icon, title, explanation and an optional copyable command: empty states teach |
| `Skeleton` `SkeletonRows` `Spinner` | loading | skeletons reserve space so there is no layout shift |
| `CommandPalette` | ⌘K | `rankCommands` (prefix > substring > keyword > fuzzy) and `paletteKey` are pure and unit-tested |
| `Callout` | inline notices | tones `info` `warn` `danger` `agent` |
| `Segmented` | view switches | split/unified, write/preview |

Shared widgets built on top of these live in `src/shell/common.tsx`: `Markdown` (sanitized), `TimeAgo`, `LevelBadge`/`LevelExplainer` (autonomy levels with their meaning), `LoopSteps` (the loop visualized), `QueryState` (loading → friendly 404 → 5xx with retry → content), `RefPicker`, `StateFilter`, `FeatureHint`.

## Usage rules

1. One primary button per view. Destructive actions use `danger` and confirm through a dialog or an undoable toast.
2. Lists that the user navigates are `.list` rows (links), not tables; `j`/`k`/Enter work on them through `useListNavigation`.
3. Every control is labelled: visible text, `aria-label` for icon buttons, `Field` for inputs.
4. Relative times always carry the absolute time in `title`; ids are shortened in prose and copyable in full.
5. Empty states say what the thing is for and show the exact command or button that creates the first one.
6. Nothing that an agent did is rendered the same way as what a human did: use the agent tone, the lane headers and the provenance badges.
7. Pages must work at 375 px: the top bar collapses to icons, the code and diff trees hide behind buttons, tables drop `hideSm` columns.
