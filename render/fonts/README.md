# Bundled fonts

The raster path embeds these fonts so `Render` gives the same bytes on every
till, whatever fonts Windows has installed.

| File | Project |
| --- | --- |
| `NotoSans-Regular.ttf`, `NotoSans-Bold.ttf` | https://github.com/notofonts/latin-greek-cyrillic |
| `NotoSansKannada-Regular.ttf`, `NotoSansKannada-Bold.ttf` | https://github.com/notofonts/kannada |
| `NotoSansDevanagari-Regular.ttf`, `NotoSansDevanagari-Bold.ttf` | https://github.com/notofonts/devanagari |
| `NotoSansTamil-Regular.ttf`, `NotoSansTamil-Bold.ttf` | https://github.com/notofonts/tamil |

All are the hinted TTF builds from
https://github.com/notofonts/notofonts.github.io (`fonts/<family>/hinted/ttf/`),
downloaded on 11 November 2026. Each is Copyright The Noto Project Authors
and licensed under the SIL Open Font License 1.1 (`OFL.txt`). The fonts are
bundled unmodified, and their names are kept, as the licence allows for
unmodified fonts.

Changing a font changes every raster golden file: run
`go test ./render -update` and review the `.png` previews.
