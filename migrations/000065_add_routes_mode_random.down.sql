ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_mode_check;
ALTER TABLE routes ADD CONSTRAINT routes_mode_check
    CHECK (mode IN ('cheapest', 'stable', 'fixed'));
