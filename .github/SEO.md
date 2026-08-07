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

   Three property settings **apply only from the moment they are switched on** and
   never backfill. All three are set; the note is here so nobody undoes one:

   - *Admin → Data collection and modification → Data retention*: **14 months**,
     not the 2-month default, which quietly caps every year-over-year question.
   - *Admin → Data display → Custom definitions*: dimension `CTA`, scope
     **Event**, parameter `cta`. Without it the `cta_click` events still arrive
     but the parameter saying *which* CTA is unreadable — the event collapses to
     a number with no breakdown. Scope and parameter cannot be edited after
     creation, only deleted and remade.
   - *Admin → Product links → Search Console*: linked, and the *Search Console*
     collection published under Reports → Library. Linking alone adds no report;
     the collection ships unpublished. This is what joins "which query" to "what
     they did after landing" — neither tool answers that alone.

   **Internal traffic is deliberately NOT filtered.** The filter keys on IP, an
   office shares one, and colleagues visiting is traffic worth having. If that
   ever needs revisiting, define the rule but leave the filter on *Testing*: it
   tags `traffic_type` without excluding anything, which keeps the data and adds
   a dimension to split by. Unlike the three above, that choice is reversible.
2. **Search Console** — *done.* A **URL-prefix** property for
   `https://madnh.github.io/scratchpad/`, verified by the HTML-tag method; the
   token is the `google-site-verification` meta in `docs/index.html` and must
   stay there or the property un-verifies. A Domain property is not available:
   that would cover all of `github.io`, which is not ours to verify. Not the
   Google Analytics method either — the reason is in the comment beside the tag.
3. **Sitemap** — *done.* Submitted in Search Console as **`sitemap.xml`**, with
   no leading slash. A leading `/` is read as root-relative and silently drops
   the `/scratchpad/` prefix, producing `madnh.github.io/sitemap.xml`, a 404, and
   a permanent *Couldn't fetch* row that must be deleted by hand. A sitemap
   outside the property's prefix is refused whether it exists or not.
   `robots.txt` already points at the full URL, so Googlebot finds it regardless;
   submitting is what puts a *status* on it, not what makes it count.
4. **Bing Webmaster Tools** — *done.* Added by *Import from Google Search
   Console*, which carries the sitemap across too — no separate submission was
   needed, and Bing had crawled it the same day. One import covers Bing,
   DuckDuckGo and Copilot.

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
