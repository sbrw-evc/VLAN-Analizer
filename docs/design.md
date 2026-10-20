# Design guidelines

VLAN Analizer looks and behaves like the other applications of the family: Beacon (`app/web`), Ledger,
Keystone, Current and Harbor. The web app follows Ledger's `docs/design.md` and copies Keystone's tokens and
controls value for value (React + Vite, lucide icons, `motion`). The applications should read as one product: same
shell, same tokens, same controls, same light and dark themes, same phone layout.

## Shell
- **Chrome**: the window background is `--chrome`. The top bar sits on it without a bar of its own: the sidebar
  toggle (PanelLeftClose/PanelLeftOpen), the brand (red rounded logo tile + "VLAN Analizer" and a muted subtitle),
  then on the right the role pill and the user menu.
- **Page block**: the page sits in a rounded panel (`.app-main`, radius 12px, 1px `--border`, background `--bg`) to
  the right of the sidebar and scrolls on its own.
- **Sidebar** (`components/Shell.tsx`): top links **Availability** and **Networks**; accordion groups with a lucide icon
  and a chevron: **Host network** (VLAN interfaces) and **Administration** (Monitoring, Connections, Users, Zabbix).
  Groups a viewer cannot use are hidden; the API enforces the role anyway. The current page is marked by a pill
  (`--accent-soft`, accent text) that slides between links (`motion` `layoutId`). The sidebar collapses into an icon
  rail; the open groups and the rail state are kept in `localStorage` (`vlan.sidebar.*`).
- **Page**: a centred column 1100px wide with 24px padding. It starts with a **PageHead**: a 22px semibold title, a
  muted subtitle and the actions on the right. Route changes fade and lift the page by a few pixels.
- **User menu**: avatar with initials (colour from the name), name and role; the dropdown has Language
  (English/Русский) and Theme (Light/Dark) accordions and Sign out.
- **Sign-in and setup**: a centred card with the brand, an EN/RU segmented control and the theme toggle on top, then
  the form. The setup wizard shows its five steps as numbered pills and slides between steps.

## Surfaces and controls
- **Cards and tiles**: `--surface`, 1px `--border`, radius 14px, `--shadow`. Counter tiles count up to their value.
- **Tables** live inside a card: the header row is `--surface-2` with muted 12.5px semibold labels. The availability
  matrix keeps the header and the website column sticky.
- **Buttons**: 38px, radius 9px; primary is solid blue with a soft shadow, secondary white with a grey border, ghost
  without a border, danger red; `-sm` is 32px. Buttons press to 97% on click.
- **Inputs and selects**: 38px, radius 9px, `--input-border`, focus ring `--focus`; native selects get a chevron;
  checkboxes and switches are drawn with the `--check-*`/`--switch-*` tokens and spring when toggled.
- **Badges** are pills without a border: a soft fill with coloured text. Status chips carry a dot and a label, never
  colour alone: healthy `--ok`, degraded `--warn`, down `--error`, unknown and paused muted. VLAN tags are purple;
  VLANs this application manages are accent blue.
- **Segmented controls** (EN/RU, filters, addressing) have a `--segment-bg` track with a pill that slides.
- **Dialogs**: a blurred `--backdrop` and a card with radius 14px that springs in. Path diagnostics open in a drawer
  that slides in from the right. Destructive confirmations show a warning icon and "This cannot be undone."
- **Toasts**: bottom right, a card with a 4px left border in the tone colour, an icon, the message and a close button.
- **Icons**: lucide-react, 2px stroke.
- **Charts**: one series per chart, a 2px accent line that draws in, a recessive grid, a crosshair with a tooltip on
  hover and failed checks as error dots on the baseline. The measurement list is the table view.

## Motion
Springs (stiffness 420, damping 30) for pills, chevrons, dialogs and the drawer; short fades for menus; staggered
rise for tiles and cards; a pulsing dot while checks run; a packet travelling along each trunk card. Everything
respects `prefers-reduced-motion`. Switching the theme reveals the new theme in a circle from the toggle (View
Transitions) where the browser supports it.

## Phone (≤ 860px)
The sidebar becomes a menu behind the top-bar button, grids collapse to one column, the matrix scrolls sideways with
the website column pinned, and the role pill and user name hide under 560px.

## Themes
The theme is `data-theme="light|dark"` on `<html>`, kept in `localStorage` (`vlan.theme`) and applied before the first
paint by `public/theme.js` (a file, because the CSP forbids inline scripts); without a saved choice it follows the
system. All colours come from the tokens in `web/src/styles/app.css`, identical to Ledger's table of tokens.

## Language
English and Russian. The language is kept in `localStorage` (`lang`), switched on the sign-in card or in the user
menu, and the app re-renders in place. English strings are the keys (`web/src/i18n/en.ts`, `ru.ts`). Server messages
are English and translated by text; `completeness.test.ts` fails when a key or a server message has no text.

## Deliberate differences
- The logo is the family's red tile with a network glyph.
- No global search in the top bar: the matrix and the inventory have their own search fields.
- No OverlayScrollbars dependency: native scrollbars are themed with the same thumb tokens (as in Keystone).
- The API stays at `/api` and sessions are HTTP-only cookies; this application predates the family's `/api/v1`.
