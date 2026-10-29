# Hosting

The site is hosted at https://dynasty.williamfowle.com on Cloudflare, behind
Cloudflare Access.

```
Gaming PC: dsn serve (watcher + local site) ──writes──► hosted Postgres
                                                              ▲ reads
Browser / agent ─► Access ─► Worker ─┬─► container: dsn serve -hosted
                                     └─► /media/* from R2 (dsn-media)
```

- `dsn serve` on the gaming PC is unchanged. It watches saves and ingests into
  the database named by `DSN_DB` (the hosted Postgres).
- `dsn serve -hosted` runs in the container. It never ingests. It polls for
  new snapshots every 15s so open pages get the live-update toast. It indexes
  media from the bucket's `_manifest.txt`, and it stores article images in the
  bucket. Editing and `/mcp` are open to everyone Access lets in.
- The Worker (`worker/index.ts`) rejects requests without a valid Access JWT,
  serves `/media/*` from R2 and forwards everything else to the container.
  `workers.dev` and preview URLs are off, so there is no route around Access.
- The container applies `schema.sql` on start, just like `dsn serve` does.

## CI

- `.github/workflows/deploy.yml`: on push to `main`, runs `go vet` and
  `go test`, then `wrangler deploy` (it builds the image from `Dockerfile`).
- `.github/workflows/media.yml`: on push touching `media/`, uploads changed
  files and deletes removed ones. Run it manually to upload everything. Both
  modes rewrite `_manifest.txt`. A running container picks up a new manifest
  the next time it starts, which happens after 15 idle minutes.

## One-time setup

Done 2026-10-29: bucket `dsn-media`, Access team
`sweet-darkness-3d5b.cloudflareaccess.com`, app "Dynasty Sports Network"
with policy "Allow owner", one-time PIN login.

1. Create the bucket: `npx wrangler r2 bucket create dsn-media`.
2. GitHub repo secrets:
   - `CLOUDFLARE_ACCOUNT_ID`
   - `CLOUDFLARE_API_TOKEN` with Workers Scripts, Containers, Workers R2
     Storage and Workers Routes edit, plus DNS edit on `williamfowle.com` for
     the custom domain.
   - `R2_ACCESS_KEY_ID` / `R2_SECRET_ACCESS_KEY`: an R2 API token with Object
     Read & Write on `dsn-media`.
3. Access (Zero Trust → Access → Applications), a self-hosted app on
   `dynasty.williamfowle.com`:
   - Policy "Allow": your email.
   - Policy "Service Auth": a service token for agents. Agents send the
     `CF-Access-Client-Id` and `CF-Access-Client-Secret` headers to
     `https://dynasty.williamfowle.com/mcp`.
   - Copy the app's AUD tag and your team domain
     (`<team>.cloudflareaccess.com`) into `vars` in `wrangler.jsonc`. Until
     they are set, the Worker answers 403 to everything.
4. Run the Media workflow manually to fill the bucket.
5. Deploy (push to `main` or run Deploy), then set the database secret:
   `npx wrangler secret put DSN_DB`. Use the direct, TLS URL from `.env`.
