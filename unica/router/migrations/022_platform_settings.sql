-- 022: platform_settings — the behaviour switches leave the environment.
--
-- INTENT_TRIAGE and SCENE_MODE decide how every customer message is routed,
-- and until now they were read once from the router's environment at startup.
-- Changing either meant editing a unit file and restarting the router, which
-- is a poor fit for what they actually are: staged rollout switches, moved
-- off -> shadow -> on and back while someone watches what happens. The cost of
-- a restart is what stopped people from watching. DIFY_INDEXING_TECHNIQUE has
-- the same shape on the admin side.
--
-- This table is the authority for all three. It is read by two processes,
-- which is unusual here and worth stating: the router polls it on a ticker and
-- routes by what it last read, while the admin service writes it from the
-- console and reads it when creating datasets. Nothing pushes; a writer's only
-- obligation is to leave a correct row behind.
--
--   * Key/value rather than 021's shape. 021 needed version, active and
--     pushed_at because a model config is projected into Dify and the local
--     authority can legitimately disagree with what is in force out there.
--     None of that applies here. These three are platform-wide single-valued
--     enumerations with no product-line override, no projection to an external
--     system, and therefore no intermediate state to represent. A version
--     column would count edits nobody reads, and an 'active' flag would be
--     true on every row of a one-row-per-key table. What history there is
--     lives in audit_logs, where a before/after per key is exactly the record
--     an operator asks for.
--
--   * The key/value CHECK is closed, and it constrains the two columns
--     together rather than separately. 014 gave the reason for closing an
--     enumeration at the database: an unlisted value that fails loudly at
--     INSERT is how the next legitimate value gets added here, instead of
--     arriving as free text that some reader silently treats as a default.
--     Pairing key with value in one constraint is what makes that reason hold
--     for a key/value table — two independent CHECKs would happily accept
--     ('intent_triage', 'high_quality'), a row that parses and means nothing.
--
--   * No row is seeded here. Each process seeds the keys it owns on startup
--     with INSERT ... ON CONFLICT DO NOTHING, taking the value from its
--     environment, so an existing deployment keeps behaving exactly as it did
--     the moment before this migration ran. Seeding from SQL would have had to
--     guess those environments, and guessing wrong would silently reroute
--     live traffic — the one outcome this whole change exists to prevent.
--
--   * source records where a row's current value came from, and 'seed' is a
--     statement about the future as much as the past: this value arrived from
--     an environment variable during some startup, and that variable has no
--     further say. A process that finds a row already present does not
--     overwrite it from its environment. It reports the disagreement instead —
--     loudly, by name — because a deployment whose config file says one thing
--     while the database says another is exactly the silent divergence that
--     makes an operator distrust the console.
--
--   * updated_by is ON DELETE SET NULL, departing from 008_ai_agent_configs,
--     which references users(id) with no delete action. The bare form makes a
--     row here able to block the removal of a departed administrator, and
--     between "we no longer know who set this" and "you cannot delete this
--     person", the first is the honest outcome. The value stays; only the
--     attribution is lost. Attribution that must survive is in audit_logs.

CREATE TABLE IF NOT EXISTS platform_settings (
    -- One row per setting, forever. The key is the primary key because there
    -- is nothing else to distinguish two rows by: no scope, no version.
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    -- 'seed'    — written on startup from a process's environment variable.
    -- 'console' — written by an administrator through the platform page.
    source     TEXT NOT NULL,
    -- Free text from the console, e.g. why a switch was moved. Never required.
    note       TEXT,
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT platform_settings_source_check
        CHECK (source IN ('seed', 'console')),

    -- Key and value are constrained together: see the header. Adding a
    -- setting means adding a branch here, which is the point — the new key is
    -- rejected until someone has written down what its legal values are.
    CONSTRAINT platform_settings_key_value_check CHECK (
        (key = 'intent_triage'           AND value IN ('off', 'shadow', 'on'))
        OR (key = 'scene_mode'           AND value IN ('off', 'shadow', 'on'))
        OR (key = 'dify_indexing_technique' AND value IN ('high_quality', 'economy'))
    )
);
