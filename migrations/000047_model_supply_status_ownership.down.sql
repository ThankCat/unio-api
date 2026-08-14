UPDATE public.route_model_offerings
SET disabled_reason = 'model_disabled'
WHERE disabled_reason = 'model_delisted';

ALTER TABLE public.route_model_offerings
    DROP CONSTRAINT route_model_offerings_disabled_reason_check;

ALTER TABLE public.route_model_offerings
    ADD CONSTRAINT route_model_offerings_disabled_reason_check CHECK (
        disabled_reason IS NULL
        OR disabled_reason = ANY (ARRAY[
            'manual_unselected'::text,
            'model_disabled'::text,
            'binding_disabled'::text,
            'channel_disabled'::text,
            'route_channel_removed'::text,
            'channel_protocol_changed'::text,
            'migration_backfill'::text
        ])
    );
