---
name: omnius
description: Use when the user asks about Omnius — his media server (movies, series, live TV channels, torrents, subtitles) at api.omnius.lol. Query the API with bash+curl to check what's in the library, search, look at what's featured, or read analytics.
---
Omnius is the user's own media server. Talk to it with the bash tool:
`curl -s` + `jq`. Base URL: `https://api.omnius.lol/api/v2`.

Every response is a YTS-style envelope: `{"status":"ok","status_message":…,
"data":{…}}` — read `.data`. **Always filter through jq before replying**
(pick the few fields that answer the question — title, year, rating); never
paste a raw JSON dump into chat.

## Verified endpoints (public, read-only)

- `search.json?query=<q>` — unified search across movies, series, channels:
  `.data.{movies,series,channels}`.
- `list_movies.json?query_term=<q>&limit=<n>&page=<n>` — library movies
  (YTS-compatible): `.data.movie_count`, `.data.movies[]`.
- `movie_details.json?movie_id=<id>` · `movie_suggestions.json?movie_id=<id>`
  · `franchise_movies.json`.
- `home.json` — what's featured: `.data.hero_slider[]` and home sections.
- `list_series.json` · `series_details.json` · `season_episodes.json`.
- `list_channels.json` · `channel_epg.json` · `channels_by_country.json` —
  live TV; `channel_countries.json`, `channel_categories.json`.
- `curated_lists.json` · `curated_list.json`.
- `analytics/top-movies` — most-watched.
- `subtitles/search` · `subtitle_languages`.

Example — "do we have Dune?":

    curl -s 'https://api.omnius.lol/api/v2/search.json?query=dune' \
      | jq '.data | {movies: [.movies[] | {title, year}], series: [.series[] | .title]}'

An empty `movies: []` means it's not in the library — say so plainly, and
offer to check the online sources (`search_online.json?query_term=<q>`)
which search YTS/EZTV for candidates without importing anything.

## Mutations — ask first

Streaming control (`POST stream/start|stop`) and everything under `/admin`
(imports, deletes, curated-list edits, channel sync) changes server state and
admin routes need a login session Donna does not hold. Never call them on
your own initiative — if the user asks for an admin action, say what it
needs and let him do it or hand you credentials explicitly for that one task.

If an endpoint or parameter here turns out wrong (the server evolves), note
what you actually observed and adapt — the envelope and the read-only /api/v2
shape are the stable parts.
