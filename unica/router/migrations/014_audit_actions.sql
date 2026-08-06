-- 014: extend the audit action vocabulary.
--
-- 009 fixed the action set at create/update/delete, which fit a CRUD-only
-- admin. The ontology surface added three mutations that are not any of
-- those: publish and rollback (versioned activation, where "update" would
-- hide which direction the change went) and review (a verdict on evidence,
-- not an edit). The audit trail is the record a dispute reaches for, so the
-- action names must say what actually happened.
--
-- The constraint stays closed on purpose: an unlisted action failing loudly
-- at insert is how the next new verb gets added here instead of slipping in
-- as free text.
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS audit_logs_action_check;
ALTER TABLE audit_logs ADD CONSTRAINT audit_logs_action_check
    CHECK (action IN ('create', 'update', 'delete', 'publish', 'rollback', 'review'));
