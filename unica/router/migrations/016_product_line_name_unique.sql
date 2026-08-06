-- 016: make a product line's name unique.
--
-- Onboarding resolves a product line by name and creates it when the lookup
-- comes back empty. 002 declared name without a unique constraint, so two calls
-- carrying the same name can both find nothing and both insert: one customer
-- ends up with two lines, and every later get-or-create picks whichever row the
-- query happens to return first, which is how a customer's Chatwoot binding and
-- their Dify binding end up on different rows.
--
-- The index is what turns that race into a failed insert the caller can retry,
-- instead of a duplicate nothing reports. It also backs the lookup itself.
--
-- IF NOT EXISTS leaves a database that already carries the index alone; a
-- database that already holds duplicate names must merge them first, and the
-- migration failing loudly is how that gets noticed.

CREATE UNIQUE INDEX IF NOT EXISTS idx_product_lines_name ON product_lines(name);
