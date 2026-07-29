-- +goose Up
-- Canonicalize model-derived file projections independently of 0015 so
-- databases that already applied file enrichment receive the stronger
-- persistence boundary.

-- +goose StatementBegin
UPDATE files
   SET caption = btrim(
       caption,
       U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
   ),
       summary = btrim(
       summary,
       U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
   )
 WHERE (
        caption IS NOT NULL
        AND caption IS DISTINCT FROM btrim(
            caption,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )
   )
    OR (
        summary IS NOT NULL
        AND summary IS DISTINCT FROM btrim(
            summary,
            U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
        )
   );

ALTER TABLE files
    ADD CONSTRAINT files_caption_canonical_model_text
        CHECK (
            caption IS NULL
            OR (
                caption = btrim(
                    caption,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )
                AND char_length(caption) BETWEEN 1 AND 2000
            )
        ),
    ADD CONSTRAINT files_summary_canonical_model_text
        CHECK (
            summary IS NULL
            OR (
                summary = btrim(
                    summary,
                    U&'\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'
                )
                AND char_length(summary) BETWEEN 1 AND 2000
            )
        );
-- +goose StatementEnd

-- +goose Down
-- Outer whitespace removed by Up is intentionally not reconstructed. Dropping
-- the additive constraints restores the exact v15 write semantics.

-- +goose StatementBegin
ALTER TABLE files
    DROP CONSTRAINT IF EXISTS files_summary_canonical_model_text,
    DROP CONSTRAINT IF EXISTS files_caption_canonical_model_text;
-- +goose StatementEnd
