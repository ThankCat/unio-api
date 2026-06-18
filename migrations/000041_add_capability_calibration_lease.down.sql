ALTER TABLE capability_calibration_state
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS locked_by;
