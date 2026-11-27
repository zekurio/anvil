# Icons

The favicon and app icons in `web/public` are static files, committed as
rendered. Nothing generates them at build time.

- `web/public/favicon.svg` is the source of the rounded tile: the favicon, and
  `favicon.ico`, `icon-192.png` and `icon-512.png`.
- `apple-touch-icon.svg` is full bleed, because iOS rounds the corners itself.
- `icon-maskable.svg` is full bleed with the mark inside the central 80%
  circle that Android may crop to.

The mark is the path `MARK` in `web/src/ui/icon.tsx`. After changing a source,
render the files again from `web/`:

```sh
nix shell nixpkgs#librsvg nixpkgs#imagemagick nixpkgs#oxipng -c sh -c '
  for s in 16 32 48; do rsvg-convert -w $s -h $s public/favicon.svg -o /tmp/f$s.png; done
  magick /tmp/f16.png /tmp/f32.png /tmp/f48.png public/favicon.ico
  rsvg-convert -w 192 -h 192 public/favicon.svg -o public/icon-192.png
  rsvg-convert -w 512 -h 512 public/favicon.svg -o public/icon-512.png
  rsvg-convert -w 512 -h 512 icons/icon-maskable.svg -o public/icon-maskable-512.png
  rsvg-convert -w 180 -h 180 icons/apple-touch-icon.svg -o public/apple-touch-icon.png
  oxipng -q -o 4 --strip safe public/*.png'
```
