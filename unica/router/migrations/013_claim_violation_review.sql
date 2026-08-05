-- Migration 013: give claim_violations a review outcome.
--
-- Since migration 011 the table has been write-only. The validator records every
-- conflict it finds and nothing ever reads a row back, which means the evidence
-- accumulates without anybody deciding what it proves. Evidence nobody triages is
-- not evidence, it is storage.
--
-- The columns added here turn each row into a piece of work with a verdict, and
-- the verdict is what closes the loop:
--
--   ontology_wrong  the recorded fact is wrong -- the fix belongs in the ontology,
--                   and the same wrong fact is being injected into every answer.
--   model_wrong     the ontology was right and the answer was not -- the
--                   validator caught what it exists to catch.
--   false_positive  both were right and the validator misread them -- the fix
--                   belongs in the validator, and until it lands this rule is
--                   costing good answers.
--
-- Without that distinction every violation looks like a model defect, and the two
-- fixes actually available to an operator are invisible. 'pending' is the default
-- so existing rows land in the queue rather than being silently counted as judged.

ALTER TABLE claim_violations
    ADD COLUMN IF NOT EXISTS review_status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS reviewed_by   TEXT,
    ADD COLUMN IF NOT EXISTS reviewed_at   TIMESTAMPTZ;

-- The vocabulary is closed and enforced by the database: a review outcome that
-- the analysis queries do not recognise is worse than no review at all, because
-- it looks handled while contributing to nothing.
--
-- ADD CONSTRAINT has no IF NOT EXISTS, so the constraint is dropped first to keep
-- this file replayable.
ALTER TABLE claim_violations
    DROP CONSTRAINT IF EXISTS claim_violations_review_status_check;

ALTER TABLE claim_violations
    ADD CONSTRAINT claim_violations_review_status_check
    CHECK (review_status IN ('pending', 'ontology_wrong', 'model_wrong', 'false_positive'));

-- The review queue is always "one product line, one status, newest first", which
-- idx_claim_violations_line_created cannot serve: it has to scan every violation
-- ever recorded for the line to find the handful still pending. Putting
-- review_status ahead of created_at means the queue costs an index range scan
-- regardless of how much history has already been judged.
CREATE INDEX IF NOT EXISTS idx_claim_violations_review_queue
    ON claim_violations(product_line_id, review_status, created_at DESC);
