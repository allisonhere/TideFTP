# site/

Static landing pages for the Tide family of terminal apps. Plain HTML — no
build step, no dependencies. Fonts load from Google Fonts at view time;
everything else is inline.

```
site/
  index.html          the Tide family overview
  tide/index.html      Tide — terminal RSS reader
  tidemail/index.html  TideMail — terminal email client
  tideftp/index.html   TideFTP — terminal SFTP/FTP/FTPS client
  whatthedock/index.html  WhatTheDock — terminal Docker client
```

Cross-links are relative, so the tree works served from any base path.

## Preview locally

```bash
python3 -m http.server -d site 8000   # then open http://localhost:8000
```

## Publish with GitHub Pages

GitHub Pages' "Deploy from a branch" only offers the repo root or `/docs`,
not `/site`. Two ways to serve this folder:

- Rename `site/` to `docs/` and set Pages → *Deploy from branch* → `main` /
  `/docs`.
- Keep `site/` and add a Pages **Actions** workflow that uploads it:
  `actions/upload-pages-artifact@v3` with `path: site`, then
  `actions/deploy-pages@v4`.

The Tide (RSS) page is built from limited information — flesh it out once
that repo's README is to hand.
