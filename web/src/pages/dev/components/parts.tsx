import type { JSX } from "@solidjs/web";

/** One titled group of the gallery. */
export function Section(props: { id: string; title: string; children: JSX.Element }) {
  return (
    <section class="gal-section" data-section={props.id}>
      <h2>{props.title}</h2>
      {props.children}
    </section>
  );
}

/** One component, on a `surface` panel unless `bare`. */
export function Item(props: { title: string; bare?: boolean; flush?: boolean; children: JSX.Element }) {
  return (
    <div class="gal-item">
      <h3>{props.title}</h3>
      <div class={{ "gal-panel": !props.bare, "gal-panel-flush": props.flush }}>{props.children}</div>
    </div>
  );
}
