# Website contributor instructions

These instructions apply to `website/` in addition to the repository root
`AGENTS.md`.

The published documentation is a Hugo site under `website/content/docs/`.
User-visible commands, flags, config, environment variables, security behavior,
and lifecycle semantics must be updated here with the implementation.

## Authoring

- Preserve each page's frontmatter (`weight`, `title`, `description`, `icon`,
  `toc`, and `draft`).
- Keep small topics as single pages. Use a section directory with an `_index.md`
  landing page and weighted focused subpages for larger topics.
- Use Hugo `{{< relref >}}` shortcodes for internal cross-page links so broken
  references fail the site build. Use ordinary Markdown links for repository
  source files that are not site pages.
- Do not copy design-history detail into user docs. Document shipped behavior,
  defaults, constraints, and recovery steps in user terms.
- Avoid volatile inventory counts or implementation details that will drift
  without changing user behavior.

`website/content/docs/capabilities.md` contains a generated marker-delimited
region. Change `internal/capabilities/capabilities.json` and regenerate it from
the repository root with `go test ./internal/capabilities -update`; never edit
the generated region by hand.

## Verify

From the repository root, run:

```bash
make package-graph-check
make docs
node --test website/tests/*.test.mjs
```

The package-dependency data is committed. `make docs` installs locked npm
dependencies for frontend assets, then consumes the package graph without
modifying it; use `make package-graph` only when the graph inputs change.
`make docs-faro-smoke` builds with a dummy Faro collector endpoint so Hugo
bundles the npm-managed Faro entry. The Node test covers interactive
documentation assets. Use `make docs-serve` for a local live preview. The
extended Hugo version and Dart Sass used by CI are defined in
`.github/workflows/docs.yml`.
