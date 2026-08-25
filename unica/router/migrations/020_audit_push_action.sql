-- 020: add "push" to the audit action vocabulary.
--
-- 014 closed the action set at create/update/delete/publish/rollback/review
-- and said the next new verb belongs here rather than as free text. The
-- platform prompt push is that verb. It is not "publish": publishing cuts a
-- revision in the local authority, pushing projects one into a tenant's Dify
-- app, and this increment made those two separate halves with a real state
-- between them (a revision published and not yet in effect). An entry that
-- called both "publish" could not answer which half a dispute is about.
--
-- The row is written for failures too, so this constraint is what decides
-- whether a refused push leaves a trace at all.
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS audit_logs_action_check;
ALTER TABLE audit_logs ADD CONSTRAINT audit_logs_action_check
    CHECK (action IN ('create', 'update', 'delete', 'publish', 'rollback', 'review', 'push'));
