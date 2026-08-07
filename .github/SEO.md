# SEO & traffic tracking

Operational notes for the landing page (`docs/`, served by GitHub Pages at
<https://madnh.github.io/scratchpad/>). Nothing here affects the binary.

## What measures what

| Question | Where it is answered |
| --- | --- |
| Which search queries show the page, and how often is it clicked? | Google Search Console |
| Is the page indexed? Any crawl error? | Search Console → Pages / URL Inspection |
| How many people arrive, from where, and how far do they read? | Google Analytics 4 |
| Which call-to-action sends them to the repo? | GA4 → event `cta_click`, dimension `cta` |
| How many actually install? | **Not measurable here** — see the blind spot below |

Search Console and GA4 answer different questions and disagree on purpose: the
first counts what Google *showed*, the second counts what a browser *ran*. A gap
between them is normal (ad blockers, prefetch, bots), not a bug to chase.

## One-time setup

1. **GA4** — *done.* Property `scratchpad` with a Web data stream on
   `https://madnh.github.io/scratchpad/`; Measurement ID `G-QYX6BHNFPR`, in
   `docs/index.html`. The loader there runs only on the hosts in its `HOSTS`
   list, so forks and local servers stay out of the numbers.
   Leave *Enhanced measurement* on — it supplies scroll depth and outbound clicks
   without any more code.

   Then three settings that **apply only from the moment they are switched on**.
   None of them backfills, so each day they are left alone is a day of data that
   cannot be recovered later:

   - *Admin → Data settings → Data retention*: change **2 months → 14 months**.
     The default quietly caps every year-over-year question you will ever ask.
   - *Admin → Custom definitions → Create custom dimension*: name `CTA`, scope
     **Event**, parameter `cta`. Without this the `cta_click` events still
     arrive, but the parameter that says *which* CTA is unreadable in reports —
     the event becomes a number with no breakdown.
   - *Admin → Product links → Search Console*: link the verified property, then
     publish the *Search Console* report collection under Library. This is what
     joins "which query" to "what they did after landing"; neither tool can
     answer that alone.
2. **Search Console** — *done.* A **URL-prefix** property for
   `https://madnh.github.io/scratchpad/`, verified by the HTML-tag method; the
   token is the `google-site-verification` meta in `docs/index.html` and must
   stay there or the property un-verifies. A Domain property is not available:
   that would cover all of `github.io`, which is not ours to verify. Not the
   Google Analytics method either — the reason is in the comment beside the tag.
3. **Sitemap** — in Search Console, submit `sitemap.xml`. It is already linked
   from `robots.txt`, but submitting is what puts a *status* on it.
4. **Bing Webmaster Tools** — add the site and choose *Import from Google Search
   Console*. Two minutes, and it covers Bing, DuckDuckGo and Copilot at once.

## What to expect, and what to watch

The word **scratchpad** is a common noun with an encyclopedia entry and several
same-named repositories. Ranking for it alone is not a realistic target and is
not worth measuring against. What can be won is the long tail that describes what
this actually does — the phrasings to watch in Search Console's Queries tab:

- *AI agents talk to each other*, *agent to agent communication*
- *MCP server for agent coordination*, *shared context between AI agents*
- *stop copy pasting between AI chats*
- *claude code codex communication*

If a query in that family gets impressions but no clicks, the title and meta
description are the fix — they are the only thing a searcher sees.

## Maintenance rules

- **`docs/sitemap.xml` carries a `lastmod`.** Update it when `index.html`
  changes in a way a reader would notice. A date that never moves is worse than
  no date: it tells a crawler the page is stale every time it checks.
- **Six places hold the site's own address** — `<link rel="canonical">`, the
  `og:url` meta, the JSON-LD `url`, `robots.txt`, `sitemap.xml`, and the `HOSTS`
  list in the GA4 loader. Moving to a custom domain means changing all six and
  adding a `docs/CNAME`. Miss `HOSTS` and the page keeps working while silently
  recording nothing — the one failure here that does not announce itself.
  It also means a *new* Search Console property; history does not follow a domain
  move on its own, so use the Change of Address tool if it ever happens.
- **The GA4 ID lives in exactly one place** in `docs/index.html`. Keep it that way.

## The blind spot

`curl … | sh` never runs a browser, so an install through `install.sh` is
invisible to both tools above. The page can tell you someone *read* the install
step; it cannot tell you they ran it. The nearest real signal is release asset
download counts:

```sh
gh api repos/madnh/scratchpad/releases --jq \
  '.[] | {tag: .tag_name, downloads: ([.assets[].download_count] | add)}'
```

Stars and forks are in the repo's own Insights → Traffic, which also shows
referrers for the last 14 days and keeps no history beyond that. Anything you
want to compare year-over-year has to be written down somewhere else.
