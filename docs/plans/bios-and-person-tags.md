# Plan: player/coach bios and player/coach article tags

Status: **draft for review — no code changed yet.**

## What exists today (verified in the code)

- **Articles** (`internal/store/articles.go`, `schema.sql` `article`): dynasty-scoped, not branch-scoped. Team tags are `team_ids int[]` (TeamIndex). There is **already a `person_ids uuid[]` column**, and `SaveArticle` stores it. Nothing sets or reads it: no editor field, no MCP arg, no filter. There's no coach column.
- **Team news**: `team()` in `handlers.go:938` calls `Articles(ctx, c, ArticleFilter{Team: id, Limit: 5})`, which filters on `$3 = ANY(team_ids)`. `team.html` shows a "News" card that links to `/news?team=<id>`.
- **Editor** (`editor.html`): the Markdown toolbar, preview (`POST /write/preview` → `renderMarkdown`), and image upload (`POST /write/upload` → `media/articles/` or R2 via `putMedia`) all live in one inline `<script>`. Teams are picked from a `<select multiple>`.
- **Edit permission**: `canEdit` allows loopback requests, or any request when `BehindAccess` is set (the hosted site). `action()` also requires same-origin.
- **MCP** (`mcparticles.go`): `get_article` and `save_article` accept `teams`. The `search` tool returns teams and players, but no coaches.
- **Player identity**: `person.id` is a minted UUID. **Coach identity** is `coach_season.coach_key` = `lower(first|last|asset)`. It's a string derived from attributes, and `/coach/{key}` is built from it.
- **⚠ Re-ingestion gap**: `dsn reingest -into <empty db>` replays the archive into a fresh database. That mints **new person UUIDs**, and it **doesn't copy articles at all**. Every editorial row (articles today, bios and person tags after this work) would be lost or orphaned on a reingest. That breaks "bios must survive re-ingestion," so this plan fixes it.

## 1. Schema (appended to `schema.sql`, idempotent like the rest)

```sql
-- Editorial bios: written on the site, dynasty-scoped (never branch-scoped,
-- so a revert never hides them), keyed to stable identities only.
CREATE TABLE IF NOT EXISTS person_bio (
    dynasty_id  int  NOT NULL REFERENCES dynasty(id),
    person_id   uuid NOT NULL REFERENCES person(id),
    body        text NOT NULL DEFAULT '',     -- Markdown
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dynasty_id, person_id)
);
CREATE TABLE IF NOT EXISTS coach_bio (
    dynasty_id  int  NOT NULL REFERENCES dynasty(id),
    coach_key   text NOT NULL,                -- CoachKey(first, last, asset)
    body        text NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dynasty_id, coach_key)
);

-- Coach tags on articles (person_ids already exists).
ALTER TABLE article ADD COLUMN IF NOT EXISTS coach_keys text[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS article_person ON article USING gin (person_ids);
CREATE INDEX IF NOT EXISTS article_coach  ON article USING gin (coach_keys);
```

Design choices:
- **Two typed tables, not one polymorphic `bio(kind, id text)`.** `person_bio` can foreign-key `person(id)`, and each key column has the right type.
- **Arrays for tags, not join tables.** This matches `team_ids` and the existing `person_ids`, keeps `SaveArticle` a single statement, and GIN indexes make `= ANY(...)` lookups cheap. The trade-off is that arrays can't have foreign keys, so the handlers validate tags when they're written (see §4).
- Saving an empty bio deletes that `person_bio` row. That's the author's call, the same as deleting an article. It's editorial data, not game history, so rule 4 doesn't apply. Revision history is an open question (Q3).

## 2. Store (`internal/store`)

New file `bios.go`:
- `Bio(ctx, c, personID) (string, time.Time, error)` and `SaveBio(ctx, c, personID, body)` (upsert; empty body deletes the row).
- `CoachBio(ctx, c, key)` and `SaveCoachBio(ctx, c, key, body)`.
- `SaveBio` checks that `person.dynasty_id = c.Dynasty`, so a bio can't be written against another dynasty's player.

`articles.go`:
- `Article.CoachKeys []string`, added to `articleCols`, `scanArticle`, and both `SaveArticle` statements.
- `ArticleFilter` gains `Person uuid.UUID` (Nil = any) and `Coach string` ("" = any). The query adds `AND ($7::uuid IS NULL OR $7 = ANY(person_ids)) AND ($8 = '' OR $8 = ANY(coach_keys))`, passing nil when `Person == uuid.Nil`.

`queries.go` / `coaches.go`:
- `PeopleRefs(ctx, c, ids []uuid.UUID) ([]PlayerRef, error)` loads names, positions, and latest teams for chips and the story page. It's the same lateral join as `SearchPeople`, with `p.id = ANY($n)`.
- `SearchCoaches(ctx, c, term) ([]CoachRef, error)` returns distinct `coach_key` values from `coach_season` on the active branch, latest season per key, matched on name, and includes position and team.
- `CoachRefs(ctx, c, keys []string)` returns display names for tagged coaches.

## 3. Bios on the site

**Rendering.** Both `player()` and `coach()` load the bio inside their existing `parallel(...)`. The template gets `"Bio": renderMarkdown(body)`, the same goldmark config articles use (GFM plus Typographer, raw HTML dropped). A new partial, `{{template "biocard" .}}` in `partials.html`, renders a card titled "Bio" with `class="story-body"`, so images, headings, and quotes look like they do in an article. It goes between the header `<dl class="bio">` and the stats grid on both pages. With no bio, readers see nothing and editors see a small "Add bio" link. With a bio, editors also see an "Edit bio" link.

**Editing: where and who.**
- Routes: `GET /player/{id}/bio` uses `s.page(s.bioEditor)`, `POST /player/{id}/bio` uses `s.action(s.saveBio)`, and `/coach/{key}/bio` works the same way. These sit next to the subject's page rather than under `/write`, because a bio belongs to its player or coach, not to the news feed. Saving redirects back to the player or coach page.
- Who can edit: the same `canEdit` gate as articles. That's loopback locally, or anyone who got through Cloudflare Access on the hosted site. Writes go through `action()`, which also enforces same-origin. No new auth model.
- Template `bioeditor.html` holds the subject's name and headshot, the Markdown toolbar, a textarea, Preview, Save, and Cancel. **Refactor:** the inline toolbar, preview, and upload script moves out of `editor.html` into `static/mdeditor.js`, which both editors load. That keeps one copy of the Markdown UX, so bios support exactly what articles do. Preview reuses `POST /write/preview`. Image upload reuses `POST /write/upload`, unchanged. Files go to `media/articles/` (or R2 under `articles/`), so the Worker's `media.r2` bridge and the media manifest need no changes. See Q4 if you'd rather have a separate `bios/` prefix.

**Real-world rule.** Bios are free text the author writes. The site never fills a bio from game data. The editor placeholder and the MCP description both say to keep bios to real-world facts (no ratings, dev traits, or similar).

## 4. Player and coach tags on articles

**Editor UX** (`editor.html`, below the Teams card). A new "Players & Coaches" card holds:
- A search box. Typing 2+ characters calls `GET /write/people?q=…`, a new editor-only JSON endpoint gated by `canEdit`. It returns up to 10 players (`SearchPeople`: name, position, class, team) and up to 5 coaches (`SearchCoaches`: name, role, team). The fetch is debounced at about 200 ms.
- A results dropdown that works with the keyboard (↑/↓/Enter) and the mouse. Picking a result adds a removable chip: "Jalen Smith · QB · Texas State" or "Coach Pat Doe · HC · TCU".
- Behind each chip, a hidden input: `person=<uuid>` or `coach=<key>`.
- On edit, the `editor()` handler loads chips for the stored tags through `PeopleRefs` and `CoachRefs`.
- Nice to have: on a game story (`?game=`), offer the head coaches of both teams as one-click suggestions.

**Save** (`saveStory`): parse `f["person"]` as UUIDs and `f["coach"]` as keys. Drop anything that doesn't resolve in this dynasty: one `SELECT id FROM person WHERE dynasty_id = $1 AND id = ANY($2)`, and the coach query works the same way. Dedupe and keep the order.

**Read side**:
- `player()`: `Articles(ArticleFilter{Person: id, Team: -1, Season: -1, Limit: 5})` goes into `"Stories"`. `coach()` does the same with `Coach: key`.
- The "News" card markup from `team.html` moves into a shared partial, `{{template "newscard" ...}}`, used by the team, player, and coach pages. Its "All stories" link goes to `/news?player=<uuid>` or `/news?coach=<key>`.
- `news()` accepts the `player` and `coach` query params alongside `team` and titles the page "<Name> News".
- `story()`: the "Tagged" strip shows player and coach links next to team links. `StoryCard` gains `People []PlayerRef` and `Coaches []CoachRef`, filled only on the story page, so list pages don't do extra lookups.
- Drafts never show on player or coach pages, the same as team pages (`Drafts: false`).
- Spoilers: tags don't change visibility. A story is already gated by `published`.

## 5. MCP (`mcparticles.go`, `mcp.go`)

- `save_article` gains `players` (array of player UUIDs, as in `/player/<uuid>`) and `coaches` (array of coach keys, as in `/coach/<key>`). Semantics match `teams`: if omitted, the stored value is kept; if given, it replaces the stored value. Unknown ids return an error that points to `search`.
- `get_article` lists `players:` and `coaches:` with names and paths.
- `search` adds a "## Coaches" section: `- Pat Doe, Head Coach, TCU: /coach/<key>`. Agents need this to tag coaches.
- **New bio tools.** The rendered bio already shows up through `read_page`, but agents need the raw Markdown to revise a bio:
  - `get_bio {player | coach, dynasty}` returns the stored Markdown, or an empty bio.
  - `save_bio {player | coach, body, dynasty}` upserts. An empty body clears the bio. The description says "user-authored; real-world facts only; write only when the user asked."
  - Exactly one of `player` or `coach` is required.
- Update `mcpInstructions` and the `site_overview` page guide to mention bios and person/coach tags.

## 6. Making editorial data survive reingest and identity work

This applies to all editorial data: articles, tags, and bios.

- **Reingest copy step.** Once `runReingest` (`cmd/dsn/app.go`) has replayed all snapshots, it copies the source DB's `article`, `person_bio`, and `coach_bio` rows into the target:
  - **Dynasty**: remap by `league_id`, since serial ids differ between databases.
  - **Person UUIDs**: remap by matching the source person's fixed facts to the target person: `asset_name + birth_date + first/last name + home town/state`, using the same `sameOwner` check that ingest's relink already uses. If exactly one match is found, remap the id. If there are zero or several matches, don't guess. Keep the bio or tag out of the target, write a `review_flag` (`kind = 'editorial_unmapped'`) holding the source id and the text, and print a summary. This follows rule 3: never attach editorial content to the wrong person.
  - **Coach keys** are derived from name and asset, so they carry over unchanged.
  - **Team ids** are TeamIndex, so they carry over unchanged.
  - Image paths are `/media/...` and don't change.
- **Identity corrections.** There's no merge or split tool today. If one is added later, it must move `person_bio` and rewrite `article.person_ids` in the same transaction. I'll add a comment on `person_bio` and a test hook so whoever builds that tool can't miss it.
- **Branches.** Bios and tags are dynasty-scoped, so a revert never hides them. A person first seen on an abandoned branch keeps their bio, and their page still resolves through `Person()`, which is not branch-filtered.

## 7. Tests

The store tests follow the existing pattern: real Postgres, skipped when it isn't configured (see `open_test.go`).
- `store`: `SaveBio`/`Bio` round trip; an empty body deletes the row; a cross-dynasty person is rejected. `SaveArticle` round trips `PersonIDs` and `CoachKeys`; `Articles` filters by `Person` and `Coach`, and drafts are excluded. `SearchCoaches` returns one row per key, using the latest season.
- `web`:
  - `saveStory` with `person` and `coach` form values: unknown ids are dropped and the order is kept.
  - The bio POST is rejected from a non-loopback address without `BehindAccess`, and rejected when cross-origin.
  - `renderMarkdown` on a bio strips `<script>`.
  - The player and coach pages render the Bio card and the News card. The empty state hides both for readers.
  - `/write/people` is gated by `canEdit`.
- `mcp_test.go`: `save_article` with `players` and `coaches`, including validation errors. `save_bio` and `get_bio` round trip. `save_bio` requires exactly one of `player` or `coach`. `search` lists coaches.
- Reingest: a fixture test copies an article with a person tag and a bio into a fresh DB and checks that the UUIDs were remapped. It also checks that an ambiguous person produces a `review_flag`, not a silent remap. This test skips without fixtures, per the Conventions section of AGENTS.md.
- A manual check in the preview browser: tag a player, see the story on the player page, and write a bio with an uploaded image locally. On the hosted site, confirm the R2 upload path still works.

## 8. Suggested order of work (each step can ship on its own)

1. Schema, plus the store changes for tags. Tag picker in the editor, news cards on the player and coach pages, `news?player=` / `news?coach=`, MCP `players`/`coaches`, and coaches in `search`.
2. Extract `mdeditor.js`. Bio tables, store, editor routes and template, rendering on the player and coach pages, and MCP `get_bio`/`save_bio`.
3. The reingest editorial copy step with remap and review flags. This also fixes the gap where reingest drops articles today.

## Decisions so far (from review)

- **Reingest copy step (Q7):** in scope but low priority. Build the simple version (copy, remap UUIDs, flag the rest) and test that it basically works. Don't polish it.
- **Coach identity (Q1):** keep `coach_key` because live data depends on it. Known limitation: the game draws coaches from a limited pool of names and portraits, so two different coaches can share a key. They already share one `/coach/<key>` page today, so they would also share one bio and one set of tags. Say this in the bio editor's help text and in the `save_bio` description. Don't build anything to separate them.
- **JSON API vs MCP (Q5):** they're separate. `/api/*` is the site's JSON endpoints. The MCP tools read pages through `read_page`, which turns the rendered HTML page into Markdown. So a bio on the player or coach page reaches agents with no API change, and `get_bio` gives them the raw Markdown to edit.

## Open questions for you

1. **Coach identity.** Coach bios and tags would key on `coach_key` (name + portrait asset), because that's the coach identity the site uses everywhere. If the game ever changes a coach's portrait asset, the key changes and the bio and tags detach. Is that acceptable for now, or should I first mint coach UUIDs (a `coach` table plus aliases, like `person`)? That would be a separate, bigger change. I recommend accepting `coach_key` now.
2. **Auto-tagging teams.** When an author tags a player or coach, should the editor also add their current team automatically? I recommend offering it as a suggestion, not adding it automatically.
3. **Bio history.** Should bio edits overwrite in place, the way articles do, or should every saved version go into a `bio_revision` table? I recommend overwriting, to match articles.
4. **Bio images.** Should they share `media/articles/`, or get a separate `bios/` prefix? A separate prefix may need the Worker's R2 bridge and the media manifest to allow it. I recommend sharing.
5. **API and exports.** Should `/api/player/{id}` and `/player/{id}/export` include the bio Markdown? I recommend including it in the export, since it's useful context for AI chats, and leaving it out of the JSON API for now.
6. **Who edits on hosted.** Today anyone who passes Cloudflare Access can edit articles, and bios would follow the same rule. Do you want an allowlist (for example, matching the Access identity email header) for writes? I recommend keeping the current rule.
7. **Reingest step.** Is fixing the reingest gap in scope here? It's needed for "bios survive re-ingestion," and it also protects articles, which reingest drops today.
