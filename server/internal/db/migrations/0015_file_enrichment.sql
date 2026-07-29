-- +goose Up
-- File enrichment keeps user-authored values separate from derived projections
-- and records reviewable AI annotations with stable retry identities.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION mem_model_text_has_non_display_character(value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
    SELECT value COLLATE "C" ~ U&'[\00AD\034F\0600-\0605\061C\06DD\070F\0890-\0891\08E2\115F-\1160\17B4-\17B5\180B-\180F\200B-\200F\202A-\202E\2060-\206F\3164\FE00-\FE0F\FEFF\FFA0\FFF0-\FFFB\+0110BD\+0110CD\+013430-\+01343F\+01BCA0-\+01BCA3\+01D173-\+01D17A\+0E0000-\+0E0FFF]'
$$;

ALTER TABLE files
    ADD COLUMN IF NOT EXISTS user_tags text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS source_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS processor_metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

UPDATE files
   SET user_tags = tags;

-- Captions are unconfirmed, reproducible model output. Older workers could
-- persist arbitrary provider text, so upgrade drops unsafe legacy values
-- before adding the same bounded display-text guard used by current code.
-- PostgreSQL ARE has no Unicode property escape. The helper's explicit
-- Unicode 15 Cf ∪ Default_Ignorable_Code_Point class matches the Go
-- model-text validator and uses C collation for deterministic ranges.
UPDATE files
   SET caption = NULL
 WHERE caption IS NOT NULL
   AND (
        char_length(btrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) = 0
        OR char_length(btrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) > 2000
        OR caption ~ '[[:cntrl:]]'
        OR mem_model_text_has_non_display_character(caption)
        OR left(ltrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        ), 1) IN ('{', '[', '"')
        OR left(ltrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        ), 3) = '```'
        OR position('<analysis' IN lower(caption)) > 0
        OR position('</analysis' IN lower(caption)) > 0
        OR position('<think' IN lower(caption)) > 0
        OR position('</think' IN lower(caption)) > 0
        OR position('<reasoning' IN lower(caption)) > 0
        OR position('</reasoning' IN lower(caption)) > 0
        OR position('[analysis]' IN lower(caption)) > 0
        OR position('[reasoning]' IN lower(caption)) > 0
        OR lower(ltrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) LIKE 'analysis:%'
        OR lower(ltrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) LIKE 'reasoning:%'
   );

UPDATE files
   SET summary = NULL
 WHERE summary IS NOT NULL
   AND (
        char_length(btrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) = 0
        OR char_length(btrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) > 2000
        OR summary ~ '[[:cntrl:]]'
        OR mem_model_text_has_non_display_character(summary)
        OR left(ltrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        ), 1) IN ('{', '[', '"')
        OR left(ltrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        ), 3) = '```'
        OR position('<analysis' IN lower(summary)) > 0
        OR position('</analysis' IN lower(summary)) > 0
        OR position('<think' IN lower(summary)) > 0
        OR position('</think' IN lower(summary)) > 0
        OR position('<reasoning' IN lower(summary)) > 0
        OR position('</reasoning' IN lower(summary)) > 0
        OR position('[analysis]' IN lower(summary)) > 0
        OR position('[reasoning]' IN lower(summary)) > 0
        OR lower(ltrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) LIKE 'analysis:%'
        OR lower(ltrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )) LIKE 'reasoning:%'
   );

ALTER TABLE files
    DROP CONSTRAINT IF EXISTS files_source_metadata_object,
    ADD CONSTRAINT files_source_metadata_object
        CHECK (jsonb_typeof(source_metadata) = 'object'),
    DROP CONSTRAINT IF EXISTS files_processor_metadata_object,
    ADD CONSTRAINT files_processor_metadata_object
        CHECK (jsonb_typeof(processor_metadata) = 'object'),
    DROP CONSTRAINT IF EXISTS files_caption_safe_model_text,
    ADD CONSTRAINT files_caption_safe_model_text
        CHECK (
            caption IS NULL
            OR (
                char_length(btrim(
                    caption,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )) BETWEEN 1 AND 2000
                AND caption !~ '[[:cntrl:]]'
                AND NOT mem_model_text_has_non_display_character(caption)
                AND left(ltrim(
                    caption,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                ), 1) NOT IN ('{', '[', '"')
                AND left(ltrim(
                    caption,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                ), 3) <> '```'
                AND position('<analysis' IN lower(caption)) = 0
                AND position('</analysis' IN lower(caption)) = 0
                AND position('<think' IN lower(caption)) = 0
                AND position('</think' IN lower(caption)) = 0
                AND position('<reasoning' IN lower(caption)) = 0
                AND position('</reasoning' IN lower(caption)) = 0
                AND position('[analysis]' IN lower(caption)) = 0
                AND position('[reasoning]' IN lower(caption)) = 0
                AND lower(ltrim(
                    caption,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )) NOT LIKE 'analysis:%'
                AND lower(ltrim(
                    caption,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )) NOT LIKE 'reasoning:%'
            )
        ),
    DROP CONSTRAINT IF EXISTS files_summary_safe_model_text,
    ADD CONSTRAINT files_summary_safe_model_text
        CHECK (
            summary IS NULL
            OR (
                char_length(btrim(
                    summary,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )) BETWEEN 1 AND 2000
                AND summary !~ '[[:cntrl:]]'
                AND NOT mem_model_text_has_non_display_character(summary)
                AND left(ltrim(
                    summary,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                ), 1) NOT IN ('{', '[', '"')
                AND left(ltrim(
                    summary,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                ), 3) <> '```'
                AND position('<analysis' IN lower(summary)) = 0
                AND position('</analysis' IN lower(summary)) = 0
                AND position('<think' IN lower(summary)) = 0
                AND position('</think' IN lower(summary)) = 0
                AND position('<reasoning' IN lower(summary)) = 0
                AND position('</reasoning' IN lower(summary)) = 0
                AND position('[analysis]' IN lower(summary)) = 0
                AND position('[reasoning]' IN lower(summary)) = 0
                AND lower(ltrim(
                    summary,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )) NOT LIKE 'analysis:%'
                AND lower(ltrim(
                    summary,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )) NOT LIKE 'reasoning:%'
            )
        );
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS file_annotations (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id             uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    stable_key          text NOT NULL
                            CHECK (char_length(stable_key) BETWEEN 1 AND 255),
    kind                text NOT NULL CHECK (kind IN ('description', 'tag')),
    value_text          text NOT NULL
                            CHECK (
                                char_length(value_text) BETWEEN 1 AND
                                    CASE kind
                                        WHEN 'tag' THEN 64
                                        ELSE 2000
                                    END
                                AND value_text = btrim(
                                    value_text,
                                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                                )
                                AND value_text !~ '[[:cntrl:]]'
                                AND NOT mem_model_text_has_non_display_character(value_text)
                                AND left(ltrim(
                                    value_text,
                                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                                ), 1) NOT IN ('{', '[', '"')
                                AND left(ltrim(
                                    value_text,
                                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                                ), 3) <> '```'
                                AND position('<analysis' IN lower(value_text)) = 0
                                AND position('</analysis' IN lower(value_text)) = 0
                                AND position('<think' IN lower(value_text)) = 0
                                AND position('</think' IN lower(value_text)) = 0
                                AND position('<reasoning' IN lower(value_text)) = 0
                                AND position('</reasoning' IN lower(value_text)) = 0
                                AND position('[analysis]' IN lower(value_text)) = 0
                                AND position('[reasoning]' IN lower(value_text)) = 0
                                AND lower(ltrim(
                                    value_text,
                                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                                )) NOT LIKE 'analysis:%'
                                AND lower(ltrim(
                                    value_text,
                                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                                )) NOT LIKE 'reasoning:%'
                            ),
    confidence          real NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    source              text NOT NULL CHECK (source = 'model'),
    provider            text NOT NULL DEFAULT ''
                            CHECK (
                                char_length(provider) <= 255
                                AND provider !~ '[[:cntrl:]]'
                                AND NOT mem_model_text_has_non_display_character(provider)
                            ),
    processor           text NOT NULL DEFAULT ''
                            CHECK (
                                char_length(processor) <= 64
                                AND processor !~ '[[:cntrl:]]'
                                AND NOT mem_model_text_has_non_display_character(processor)
                            ),
    analysis_version    text NOT NULL
                            CHECK (
                                char_length(analysis_version) BETWEEN 1 AND 64
                                AND analysis_version !~ '[[:cntrl:]]'
                                AND NOT mem_model_text_has_non_display_character(analysis_version)
                            ),
    status              text NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'accepted', 'rejected', 'superseded')),
    state_version       bigint NOT NULL DEFAULT 1 CHECK (state_version > 0),
    decided_by_user_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_at          timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (status IN ('accepted', 'rejected') AND decided_at IS NOT NULL)
        OR
        (status IN ('pending', 'superseded') AND decided_at IS NULL)
    ),
    UNIQUE (file_id, stable_key)
);

CREATE INDEX IF NOT EXISTS idx_file_annotations_file_status
    ON file_annotations (file_id, status);
CREATE INDEX IF NOT EXISTS idx_file_annotations_pending
    ON file_annotations (file_id, created_at, id)
    WHERE status = 'pending';
-- +goose StatementEnd

-- +goose Down
-- The legacy schema cannot represent tag provenance. Restore its tags
-- projection to the user-authored subset before removing enrichment state so
-- a later re-up cannot reinterpret accepted model tags as user tags. Accepted
-- model tags are reproducible derived data; uploaded content and the remaining
-- legacy projections are preserved.

-- +goose StatementBegin
UPDATE files
   SET tags = user_tags
 WHERE tags IS DISTINCT FROM user_tags;

DROP TABLE IF EXISTS file_annotations;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE files
    DROP CONSTRAINT IF EXISTS files_summary_safe_model_text,
    DROP CONSTRAINT IF EXISTS files_caption_safe_model_text,
    DROP CONSTRAINT IF EXISTS files_processor_metadata_object,
    DROP CONSTRAINT IF EXISTS files_source_metadata_object,
    DROP COLUMN IF EXISTS processor_metadata,
    DROP COLUMN IF EXISTS source_metadata,
    DROP COLUMN IF EXISTS user_tags;

DROP FUNCTION IF EXISTS mem_model_text_has_non_display_character(text);
-- +goose StatementEnd
