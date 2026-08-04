# site assets

`og-v2.png` is the social card, rasterised by hand from `hero.svg` (both are
1200x630):

```sh
rsvg-convert -w 1200 -h 630 site/hero.svg -o site/og-v2.png
```

Regenerate it whenever `hero.svg` changes; nothing automates the coupling.

Font caveat: the SVG's stack is `ui-monospace, 'SF Mono', Menlo, ...`.
rsvg-convert resolves fonts through fontconfig, not the browser stack, so the
result depends on what the machine has installed (the committed card was
rendered on a Mac without SF Mono, so it is set in Menlo; a browser on the same
Mac resolves `ui-monospace` to SF Mono, and a Linux/CI box has neither).
Renders are therefore not reproducible across machines: treat the committed PNG
as canonical and regenerate deliberately, checking the result before
committing. Verify the SVG itself in a real browser too, not only in
rsvg-convert; librsvg silently drops elements (e.g. a mask it cannot parse)
that Chrome renders differently.

The `-v2` suffix is a cache-buster: social scrapers cache `og:image` by URL, so
a materially different card should ship under a new filename, with the
`og:image` meta tag in `index.html` updated to match.
