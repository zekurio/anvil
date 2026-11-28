import type { JSX } from "@solidjs/web";
import { For, Show } from "solid-js";

import { Icon, Logo, type IconName } from "./icon";

/* ---------- Sidebar ---------- */

export type SidebarItem =
  | {
      id: string;
      label: string;
      icon: IconName;
      count?: number | string;
      /** Makes the item a link. Without it the item is a button that reports `onChange(id)`. */
      href?: string;
    }
  | { section: string };

export interface SidebarProps {
  items: SidebarItem[];
  /** The id of the current item. */
  value?: string;
  onChange?: (id: string) => void;
  /** Version, worker count. Mono. */
  footer?: JSX.Element;
  /** Where the wordmark links to. */
  home?: string;
  /** Small ghost icon buttons at the right of the wordmark: theme, sign out. */
  actions?: JSX.Element;
}

type NavItem = Extract<SidebarItem, { id: string }>;

const asNav = (item: SidebarItem): NavItem | undefined => ("id" in item ? item : undefined);
const sectionOf = (item: SidebarItem): string => ("section" in item ? item.section : "");

function NavEntry(props: { item: NavItem; current: boolean; onSelect: () => void }) {
  const body = () => (
    <>
      <Icon name={props.item.icon} size={16} />
      <span class="av-nav-label">{props.item.label}</span>
      <Show when={props.item.count != null}>
        <span class="av-nav-count">{props.item.count}</span>
      </Show>
    </>
  );
  return (
    <Show
      when={props.item.href}
      fallback={
        <button type="button" class="av-nav-item" aria-current={props.current ? "page" : undefined} onClick={() => props.onSelect()}>
          {body()}
        </button>
      }
    >
      {(href) => (
        // Always written, "false" included: the router manages aria-current only on links that leave it out,
        // and it marks exact matches alone, which would drop the mark on /libraries/movies/rules.
        <a class="av-nav-item" href={href()} aria-current={props.current ? "page" : "false"} onClick={() => props.onSelect()}>
          {body()}
        </a>
      )}
    </Show>
  );
}

/** App navigation: the wordmark, then sections of items, 220px on `bg`. */
export function Sidebar(props: SidebarProps) {
  return (
    <nav class="av-sidebar" aria-label="Main">
      <div class="av-sidebar-brand">
        <Show when={props.home} fallback={<Logo variant="wordmark" size={22} />}>
          {(home) => (
            <a class="av-sidebar-home" href={home()} aria-label="anvil, home">
              <Logo variant="wordmark" size={22} />
            </a>
          )}
        </Show>
        <Show when={props.actions}>
          <div class="av-sidebar-actions">{props.actions}</div>
        </Show>
      </div>
      <div class="av-sidebar-nav">
        <For each={props.items} keyed={(item) => ("section" in item ? `section:${item.section}` : item.id)}>
          {(item) => (
            <Show when={asNav(item())} fallback={<div class="av-nav-section">{sectionOf(item())}</div>}>
              {(nav) => <NavEntry item={nav()} current={props.value === nav().id} onSelect={() => props.onChange?.(nav().id)} />}
            </Show>
          )}
        </For>
      </div>
      <Show when={props.footer}>
        <div class="av-sidebar-foot">{props.footer}</div>
      </Show>
    </nav>
  );
}

/* ---------- Topbar ---------- */

export interface TopbarProps {
  title: string;
  /** Path, last scan, draft state. Mono. */
  meta?: JSX.Element;
  /** A link back to the list this view came from. The flow editor uses it in place of the Sidebar. */
  back?: { href: string; label: string };
  /** The view's actions, primary last. */
  children?: JSX.Element;
}

/** The 48px bar at the top of every view: the object's name, one line of metadata, actions on the right. */
export function Topbar(props: TopbarProps) {
  return (
    <header class="av-topbar">
      <Show when={props.back}>
        {(back) => (
          <a class="av-btn av-btn-ghost av-btn-icon av-topbar-back" href={back().href} aria-label={back().label} title={back().label}>
            <Icon name="arrow-left" size={16} />
          </a>
        )}
      </Show>
      <div class="av-topbar-titles">
        <h1 class="av-topbar-title">{props.title}</h1>
        <Show when={props.meta}>
          <span class="av-topbar-meta">{props.meta}</span>
        </Show>
      </div>
      <Show when={props.children}>
        <div class="av-topbar-actions">{props.children}</div>
      </Show>
    </header>
  );
}
