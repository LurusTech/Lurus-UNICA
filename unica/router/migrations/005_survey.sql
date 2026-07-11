-- Migration 005: Add satisfaction survey fields to conversations.

ALTER TABLE conversations ADD COLUMN IF NOT EXISTS satisfaction_score SMALLINT;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS survey_sent_at TIMESTAMPTZ;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS survey_status VARCHAR(20) DEFAULT 'not_sent';
-- survey_status: 'not_sent', 'sent', 'completed', 'no_response'

CREATE INDEX idx_conversations_survey_status ON conversations(survey_status) WHERE survey_status = 'sent';
