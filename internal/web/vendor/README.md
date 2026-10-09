# Pinned browser dependencies

Fetched from official npm packages on 2026-10-09. Package versions were checked
against package.json; tarball SHA-512 integrity was verified against npm metadata.
These are unmodified upstream browser bundles (Marked's minified UMD bundle is
renamed to marked.min.js). No CDN requests, Node runtime, npm install, or asset
build step is required to build or run PAI.

| Local file | Package | Upstream file | License |
| --- | --- | --- | --- |
| marked.min.js | marked@18.1.0 | lib/marked.umd.js | MIT; marked.LICENSE |
| purify.min.js | dompurify@3.4.16 | dist/purify.min.js | MPL-2.0 OR Apache-2.0; DOMPurify.LICENSE (Apache-2.0 option) |

Upstream projects: https://github.com/markedjs/marked and
https://github.com/cure53/DOMPurify. The original license notices remain in the
bundles. Only the two JavaScript files are embedded and served; license and
provenance files remain in the source distribution.

## Verified provenance

- https://registry.npmjs.org/marked/-/marked-18.1.0.tgz
  - npm integrity: `sha512-PamYXWWWg2nboG3oX5Ffzpy3EFZROqZJK9hSlMmUnekp7TxPs5DcT95vwd46m73/exfKCRVMtgXoPMI4FEJL6w==`
  - Bundle SHA-256: `f424dcb508fdf93e0137a970cfce8f3207ea2e3f37eca5f7556a52875683632a`
- https://registry.npmjs.org/dompurify/-/dompurify-3.4.16.tgz
  - npm integrity: `sha512-sqo+pNp3qRhCIpbgRi1y8Tgk27Bo2Ry7w0dC1NBeNTdZChWjz9Xb/KOoZbRP/R6pQZ80Qw8YhXw13hWWBbMRnQ==`
  - Bundle SHA-256: `2c90a9b46d6463f26038a29b686e82bc91de01fdac9d5229e7cfe3b360134ea2`

To update, select explicit stable versions from npm, verify their package metadata
and tarball integrity, copy the browser bundles and LICENSE files without edits,
then update this record and the pinned asset tests. Run the real-DOM security
regressions described in ../README.md as well as both Go build-tag suites.
