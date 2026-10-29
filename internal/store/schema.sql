-- Dynasty Sports Network canonical schema.
-- Only text, numbers and stats live here; media is served from disk.
-- Every history row is branch-scoped: reverted data stays on its abandoned
-- branch and is never deleted (AGENTS.md rule 4).

CREATE TABLE IF NOT EXISTS schema_version (version int PRIMARY KEY);

CREATE TABLE IF NOT EXISTS dynasty (
    id           serial PRIMARY KEY,
    league_id    bigint NOT NULL UNIQUE,
    base_year    int NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Each distinct dynasty (save lineage) is fully segregated: every other table
-- is keyed by dynasty_id directly or through branch -> dynasty.
ALTER TABLE dynasty ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE dynasty ADD COLUMN IF NOT EXISTS user_team_id int NOT NULL DEFAULT -1;
ALTER TABLE dynasty ADD COLUMN IF NOT EXISTS coach_name text NOT NULL DEFAULT '';
ALTER TABLE dynasty ADD COLUMN IF NOT EXISTS save_name text NOT NULL DEFAULT '';
ALTER TABLE dynasty ADD COLUMN IF NOT EXISTS last_save_at timestamptz;

CREATE TABLE IF NOT EXISTS branch (
    id           serial PRIMARY KEY,
    dynasty_id   int NOT NULL REFERENCES dynasty(id),
    parent_id    int REFERENCES branch(id),
    fork_pos     bigint,
    status       text NOT NULL DEFAULT 'active',   -- active | abandoned
    created_at   timestamptz NOT NULL DEFAULT now(),
    abandoned_at timestamptz
);

-- One row per distinct save content ingested. The raw save is archived on disk
-- (content-addressed) so all history can be re-ingested with a newer parser.
CREATE TABLE IF NOT EXISTS snapshot (
    id              serial PRIMARY KEY,
    dynasty_id      int NOT NULL REFERENCES dynasty(id),
    branch_id       int NOT NULL REFERENCES branch(id),
    sha256          text NOT NULL,
    save_name       text NOT NULL,
    size            bigint NOT NULL,
    mod_time        timestamptz,
    archive_path    text NOT NULL,
    parser_version  text NOT NULL,
    schema_version  text NOT NULL,
    season_index    int NOT NULL,
    year            int NOT NULL,
    stage           text NOT NULL,
    week            int NOT NULL,
    week_type       text NOT NULL,
    offseason_stage int NOT NULL,
    pos             bigint NOT NULL,
    fingerprint     text NOT NULL,
    periods         jsonb,
    ingested_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (sha256, branch_id)
);
CREATE INDEX IF NOT EXISTS snapshot_branch_pos ON snapshot(branch_id, pos);

-- Anything reconciliation could not decide on its own.
CREATE TABLE IF NOT EXISTS review_flag (
    id          serial PRIMARY KEY,
    dynasty_id  int NOT NULL,
    branch_id   int,
    snapshot_id int,
    kind        text NOT NULL,
    detail      jsonb NOT NULL,
    resolved    boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS team (
    dynasty_id      int NOT NULL,
    team_id         int NOT NULL,          -- Team.TeamIndex (stable)
    short_name      text NOT NULL,
    long_name       text NOT NULL,
    display_name    text NOT NULL,
    nick_name       text NOT NULL,
    abbrev          text NOT NULL,
    asset_name      text NOT NULL,
    primary_color   text NOT NULL,
    secondary_color text NOT NULL,
    PRIMARY KEY (dynasty_id, team_id)
);

CREATE TABLE IF NOT EXISTS team_season (
    branch_id       int NOT NULL,
    season_index    int NOT NULL,
    team_id         int NOT NULL,
    conference      text NOT NULL,
    wins            int NOT NULL, losses int NOT NULL,
    conf_wins       int NOT NULL, conf_losses int NOT NULL,
    conf_standing   int NOT NULL,
    streak          int NOT NULL,
    ap_rank         int NOT NULL, coaches_rank int NOT NULL, cfp_rank int NOT NULL,
    off_rank        int NOT NULL, def_rank int NOT NULL,
    recruiting_rank int NOT NULL,
    playoff_status  text NOT NULL,
    playoff_round   text NOT NULL,
    head_coach_row  int NOT NULL,
    stats           jsonb,
    snapshot_id     int NOT NULL,
    PRIMARY KEY (branch_id, season_index, team_id)
);

-- Polls are recorded per observed week so the rankings history accumulates.
CREATE TABLE IF NOT EXISTS poll_week (
    branch_id    int NOT NULL,
    season_index int NOT NULL,
    week         int NOT NULL,
    week_type    text NOT NULL,
    poll         text NOT NULL,            -- ap | coaches | cfp
    team_id      int NOT NULL,
    rank         int NOT NULL,
    points       int NOT NULL,
    first_place  int NOT NULL,
    prev_rank    int NOT NULL,
    wins         int NOT NULL,
    losses       int NOT NULL,
    snapshot_id  int NOT NULL,
    PRIMARY KEY (branch_id, season_index, week_type, week, poll, team_id)
);

-- A person is a real player identity, minted once as a random UUID.
CREATE TABLE IF NOT EXISTS person (
    id             uuid PRIMARY KEY,
    dynasty_id     int NOT NULL,
    first_name     text NOT NULL,
    last_name      text NOT NULL,
    home_town      text NOT NULL,
    home_state     text NOT NULL,
    handedness     text NOT NULL,
    asset_name     text NOT NULL,
    birth_date     bigint NOT NULL,
    first_branch   int NOT NULL,
    first_snapshot int NOT NULL,
    first_season   int NOT NULL,
    first_team     int NOT NULL,
    provisional    boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS person_name ON person(dynasty_id, last_name, first_name);

-- Player-table rows are aliases of a person with a validity range.
CREATE TABLE IF NOT EXISTS person_alias (
    id             serial PRIMARY KEY,
    person_id      uuid NOT NULL REFERENCES person(id),
    branch_id      int NOT NULL,
    player_row     int NOT NULL,
    first_snapshot int NOT NULL,
    last_snapshot  int NOT NULL,
    open           boolean NOT NULL DEFAULT true,
    close_reason   text
);
CREATE INDEX IF NOT EXISTS person_alias_open ON person_alias(branch_id, player_row) WHERE open;
CREATE INDEX IF NOT EXISTS person_alias_person ON person_alias(person_id);

-- Latest known bio per person per season (real-world fields only).
CREATE TABLE IF NOT EXISTS player_season (
    branch_id      int NOT NULL,
    season_index   int NOT NULL,
    person_id      uuid NOT NULL,
    team_id        int NOT NULL,
    on_roster      boolean NOT NULL,
    position       text NOT NULL,
    school_year    text NOT NULL,
    redshirt       text NOT NULL,
    jersey         int NOT NULL,
    height         int NOT NULL,
    weight         int NOT NULL,
    star_rating    int NOT NULL,
    portrait       text NOT NULL,
    is_captain     boolean NOT NULL,
    injury_status  text NOT NULL,
    snapshot_id    int NOT NULL,
    PRIMARY KEY (branch_id, season_index, person_id)
);
CREATE INDEX IF NOT EXISTS player_season_team ON player_season(branch_id, season_index, team_id);

CREATE TABLE IF NOT EXISTS game (
    branch_id     int NOT NULL,
    season_index  int NOT NULL,
    game_row      int NOT NULL,
    week          int NOT NULL,
    week_type     text NOT NULL,
    status        text NOT NULL,
    played        boolean NOT NULL,
    home_team     int NOT NULL,
    away_team     int NOT NULL,
    home_score    int NOT NULL,
    away_score    int NOT NULL,
    home_quarters int[] NOT NULL,
    away_quarters int[] NOT NULL,
    home_ot       int NOT NULL,
    away_ot       int NOT NULL,
    overtime      boolean NOT NULL,
    simmed        boolean NOT NULL,
    month         int NOT NULL,
    day           int NOT NULL,
    day_of_week   text NOT NULL,
    time_of_day   int NOT NULL,
    network       text NOT NULL,
    attendance    int NOT NULL,
    temperature   int NOT NULL,
    weather       text NOT NULL,
    wind          text NOT NULL,
    wind_speed    int NOT NULL,
    stadium       text NOT NULL,
    bowl_name     text NOT NULL,
    game_of_week  boolean NOT NULL,
    home_box      jsonb,
    away_box      jsonb,
    first_snapshot int NOT NULL,
    last_snapshot  int NOT NULL,
    PRIMARY KEY (branch_id, season_index, game_row)
);
CREATE INDEX IF NOT EXISTS game_week ON game(branch_id, season_index, week_type, week);

-- Scoring summaries exist in the save only during the game's week.
CREATE TABLE IF NOT EXISTS scoring_play (
    branch_id    int NOT NULL,
    season_index int NOT NULL,
    game_row     int NOT NULL,
    seq          int NOT NULL,
    quarter      int NOT NULL,
    clock_sec    int NOT NULL,
    home_score   int NOT NULL,
    away_score   int NOT NULL,
    home_prev    int NOT NULL,
    away_prev    int NOT NULL,
    conversion   text NOT NULL,
    scoring_team text NOT NULL,
    participants jsonb NOT NULL,   -- [{personId, stats}] team leaders at the time of the score
    snapshot_id  int NOT NULL,
    PRIMARY KEY (branch_id, season_index, game_row, seq)
);

CREATE TABLE IF NOT EXISTS player_game_stat (
    branch_id    int NOT NULL,
    season_index int NOT NULL,
    game_row     int NOT NULL,
    person_id    uuid NOT NULL,
    team_id      int NOT NULL,
    category     text NOT NULL,
    stats        jsonb NOT NULL,
    snapshot_id  int NOT NULL,
    source_row   int NOT NULL,
    PRIMARY KEY (branch_id, season_index, game_row, person_id, category)
);
CREATE INDEX IF NOT EXISTS pgs_person ON player_game_stat(branch_id, person_id);

CREATE TABLE IF NOT EXISTS player_season_stat (
    branch_id    int NOT NULL,
    season_index int NOT NULL,
    person_id    uuid NOT NULL,
    team_id      int NOT NULL,
    category     text NOT NULL,
    stats        jsonb NOT NULL,
    snapshot_id  int NOT NULL,
    source_row   int NOT NULL,
    PRIMARY KEY (branch_id, season_index, person_id, team_id, category)
);
CREATE INDEX IF NOT EXISTS pss_season ON player_season_stat(branch_id, season_index, category);
CREATE INDEX IF NOT EXISTS pss_person ON player_season_stat(branch_id, person_id);

CREATE TABLE IF NOT EXISTS player_career_stat (
    branch_id   int NOT NULL,
    person_id   uuid NOT NULL,
    category    text NOT NULL,
    stats       jsonb NOT NULL,
    snapshot_id int NOT NULL,
    PRIMARY KEY (branch_id, person_id, category)
);

CREATE TABLE IF NOT EXISTS coach_season (
    branch_id     int NOT NULL,
    season_index  int NOT NULL,
    coach_row     int NOT NULL,
    first_name    text NOT NULL,
    last_name     text NOT NULL,
    position      text NOT NULL,
    team_id       int NOT NULL,
    age           int NOT NULL,
    asset_name    text NOT NULL,
    wins int NOT NULL, losses int NOT NULL,
    school_wins int NOT NULL, school_losses int NOT NULL,
    bowl_wins int NOT NULL, bowl_losses int NOT NULL,
    conf_titles int NOT NULL, natl_titles int NOT NULL,
    playoff_wins int NOT NULL, playoff_losses int NOT NULL,
    is_user       boolean NOT NULL,
    snapshot_id   int NOT NULL,
    PRIMARY KEY (branch_id, season_index, coach_row)
);

CREATE TABLE IF NOT EXISTS award (
    branch_id    int NOT NULL,
    person_id    uuid,
    first_name   text NOT NULL,
    last_name    text NOT NULL,
    team_id      int NOT NULL,
    position     text NOT NULL,
    award_type   text NOT NULL,
    period       text NOT NULL,
    period_index int NOT NULL,
    snapshot_id  int NOT NULL,
    PRIMARY KEY (branch_id, award_type, period, period_index, first_name, last_name, team_id)
);

-- League history kept by the game (past national award winners).
CREATE TABLE IF NOT EXISTS league_award (
    branch_id   int NOT NULL,
    award_type  text NOT NULL,
    first_name  text NOT NULL,
    last_name   text NOT NULL,
    position    text NOT NULL,
    team_name   text NOT NULL,
    seq         int NOT NULL,
    snapshot_id int NOT NULL,
    PRIMARY KEY (branch_id, award_type, seq)
);

CREATE TABLE IF NOT EXISTS conference_champion (
    branch_id    int NOT NULL,
    seq          int NOT NULL,
    conference   text NOT NULL,
    winner       text NOT NULL,
    loser        text NOT NULL,
    winner_score int NOT NULL,
    loser_score  int NOT NULL,
    winner_rank  int NOT NULL,
    loser_rank   int NOT NULL,
    coach_name   text NOT NULL,
    snapshot_id  int NOT NULL,
    PRIMARY KEY (branch_id, seq)
);

CREATE TABLE IF NOT EXISTS record_book (
    branch_id   int NOT NULL,
    scope       text NOT NULL,
    scope_name  text NOT NULL,
    period      text NOT NULL,
    stat_type   text NOT NULL,
    rank        int NOT NULL,
    value       int NOT NULL,
    first_name  text NOT NULL,
    last_name   text NOT NULL,
    position    text NOT NULL,
    team_name   text NOT NULL,
    year        int NOT NULL,
    snapshot_id int NOT NULL,
    PRIMARY KEY (branch_id, scope, scope_name, period, stat_type, rank)
);

CREATE TABLE IF NOT EXISTS rivalry (
    branch_id   int NOT NULL,
    row_id      int NOT NULL,
    name        text NOT NULL,
    team1 int NOT NULL, team2 int NOT NULL,
    team1_wins int NOT NULL, team2_wins int NOT NULL,
    streak_len int NOT NULL, streak_team int NOT NULL,
    snapshot_id int NOT NULL,
    PRIMARY KEY (branch_id, row_id)
);

-- Current-state tables: replaced on every snapshot of the branch.
CREATE TABLE IF NOT EXISTS injury (
    branch_id   int NOT NULL,
    person_id   uuid NOT NULL,
    team_id     int NOT NULL,
    type        text NOT NULL,
    severity    text NOT NULL,
    min_weeks   int NOT NULL,
    max_weeks   int NOT NULL,
    snapshot_id int NOT NULL,
    PRIMARY KEY (branch_id, person_id)
);

CREATE TABLE IF NOT EXISTS depth_chart (
    branch_id   int NOT NULL,
    team_id     int NOT NULL,
    position    text NOT NULL,
    slot        int NOT NULL,
    person_id   uuid NOT NULL,
    snapshot_id int NOT NULL,
    PRIMARY KEY (branch_id, team_id, position, slot)
);

CREATE TABLE IF NOT EXISTS recruit (
    branch_id      int NOT NULL,
    season_index   int NOT NULL,
    person_id      uuid NOT NULL,
    position       text NOT NULL,
    class          text NOT NULL,
    stars          int NOT NULL,
    national_rank  int NOT NULL,
    position_rank  int NOT NULL,
    state_rank     int NOT NULL,
    stage          text NOT NULL,
    committed_team int NOT NULL,
    snapshot_id    int NOT NULL,
    PRIMARY KEY (branch_id, season_index, person_id)
);

-- Events: leaving intent, position moves, departures. Intent is not departure.
CREATE TABLE IF NOT EXISTS player_event (
    branch_id    int NOT NULL,
    person_id    uuid NOT NULL,
    season_index int NOT NULL,
    kind         text NOT NULL,     -- leaving | position_change | departed
    detail       jsonb NOT NULL,
    snapshot_id  int NOT NULL,
    PRIMARY KEY (branch_id, person_id, season_index, kind, detail)
);

INSERT INTO schema_version VALUES (1) ON CONFLICT DO NOTHING;

-- v1.1: league structure, game types, coaching history, team history, articles.
ALTER TABLE team_season ADD COLUMN IF NOT EXISTS division text NOT NULL DEFAULT '';
ALTER TABLE game ADD COLUMN IF NOT EXISTS game_type text NOT NULL DEFAULT '';
ALTER TABLE coach_season ADD COLUMN IF NOT EXISTS season_wins int NOT NULL DEFAULT 0;
ALTER TABLE coach_season ADD COLUMN IF NOT EXISTS season_losses int NOT NULL DEFAULT 0;
ALTER TABLE coach_season ADD COLUMN IF NOT EXISTS seasons_with_team int NOT NULL DEFAULT 0;
ALTER TABLE coach_season ADD COLUMN IF NOT EXISTS years_coaching int NOT NULL DEFAULT 0;
ALTER TABLE coach_season ADD COLUMN IF NOT EXISTS home_state text NOT NULL DEFAULT '';
-- Coach identity: the game reuses Coach rows, so a coach is name + portrait asset.
ALTER TABLE coach_season ADD COLUMN IF NOT EXISTS coach_key text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS coach_season_key ON coach_season(branch_id, coach_key);

CREATE TABLE IF NOT EXISTS conference_season (
    branch_id    int NOT NULL,
    season_index int NOT NULL,
    name         text NOT NULL,
    champ_game   text NOT NULL,
    divisions    jsonb NOT NULL,          -- [{name, teamIds}] in the game's order
    snapshot_id  int NOT NULL,
    PRIMARY KEY (branch_id, season_index, name)
);

-- The game's own season-by-season record per team (reaches before capture).
CREATE TABLE IF NOT EXISTS team_history (
    branch_id     int NOT NULL,
    team_id       int NOT NULL,
    year          int NOT NULL,           -- calendar year
    coach_name    text NOT NULL,          -- as the game abbreviates it: "B. McWhorter"
    conference    text NOT NULL,
    division      text NOT NULL,
    wins int NOT NULL, losses int NOT NULL, ties int NOT NULL,
    conf_wins int NOT NULL, conf_losses int NOT NULL,
    final_rank    int NOT NULL,
    conf_standing int NOT NULL,
    conf_champ    boolean NOT NULL,
    postseason    jsonb NOT NULL,         -- [{bowl, playoff, round, won}]
    snapshot_id   int NOT NULL,
    PRIMARY KEY (branch_id, team_id, year)
);

-- Articles are written by hand and belong to the dynasty, not a branch, so a
-- revert never hides them. Images live under media/articles, never here.
CREATE TABLE IF NOT EXISTS article (
    id            serial PRIMARY KEY,
    dynasty_id    int NOT NULL REFERENCES dynasty(id),
    slug          text NOT NULL,
    headline      text NOT NULL,
    dek           text NOT NULL DEFAULT '',
    byline        text NOT NULL DEFAULT '',
    body          text NOT NULL DEFAULT '',
    image         text NOT NULL DEFAULT '',
    image_caption text NOT NULL DEFAULT '',
    season_index  int,
    game_row      int,
    team_ids      int[] NOT NULL DEFAULT '{}',
    person_ids    uuid[] NOT NULL DEFAULT '{}',
    published     boolean NOT NULL DEFAULT false,
    published_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS article_dynasty ON article(dynasty_id, published, published_at DESC);
CREATE INDEX IF NOT EXISTS article_game ON article(dynasty_id, season_index, game_row);

-- Game stories are either a preview (written before kickoff) or a recap.
-- Older game-linked stories have kind '' and read as recaps.
ALTER TABLE article ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT '';

-- Superseded raw saves (an older save of the same week on the same branch)
-- are deleted from data/archive to save space. The snapshot row stays; this
-- marks that its archive file is gone, so rebuild and repair tools skip it.
ALTER TABLE snapshot ADD COLUMN IF NOT EXISTS archive_pruned_at timestamptz;

-- Everyone on a team's roster at some point in a season. player_season keeps
-- only the latest bio, and the last save of a season is from the offseason,
-- after graduates, draftees and transfers have left; this keeps them on the
-- season's roster. Written from in-season saves only (offseason moves belong
-- to the next season). The bio columns are as of the player's last in-season
-- save with the team: offseason saves advance class years, so the season's
-- player_season bio would show next year's class.
CREATE TABLE IF NOT EXISTS roster_spot (
    branch_id      int NOT NULL,
    season_index   int NOT NULL,
    team_id        int NOT NULL,
    person_id      uuid NOT NULL,
    first_snapshot int NOT NULL,
    last_snapshot  int NOT NULL,
    position       text NOT NULL,
    jersey         int NOT NULL,
    school_year    text NOT NULL,
    redshirt       text NOT NULL,
    height         int NOT NULL,
    weight         int NOT NULL,
    star_rating    int NOT NULL,
    portrait       text NOT NULL,
    is_captain     boolean NOT NULL,
    PRIMARY KEY (branch_id, season_index, team_id, person_id)
);
