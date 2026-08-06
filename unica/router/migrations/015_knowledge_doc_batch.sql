-- 015: record the Dify indexing batch of each knowledge document.
--
-- Indexing progress in Dify is keyed by BATCH, not by document id. Every
-- create_by_file / update_by_file answers with a batch string, and
-- GET /datasets/{dataset_id}/documents/{batch}/indexing-status is the only way
-- to ask how that indexing went. The pipeline decoded the batch out of the
-- upload reply and dropped it, then asked for the status with the document id,
-- which is not a batch and 404s -- so no document ever moved off status
-- 'indexing' and a failed vectorisation looked exactly like a slow one.
--
-- The column is nullable because the rows written before this migration have
-- nothing to backfill from: a batch exists only in the reply to the upload that
-- created the row, and that reply is long gone. Those rows report an unknown
-- indexing state rather than an error, and regain a batch the first time their
-- content is updated.
--
-- Each upload mints a new batch that supersedes the previous one for the same
-- document, so this column is overwritten on update rather than accumulated.

ALTER TABLE knowledge_docs
    ADD COLUMN IF NOT EXISTS batch VARCHAR(255);
