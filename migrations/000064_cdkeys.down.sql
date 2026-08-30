ALTER TABLE public.ledger_entries
    DROP CONSTRAINT IF EXISTS ck_ledger_entries_balance_math;
ALTER TABLE public.ledger_entries
    DROP CONSTRAINT IF EXISTS ledger_entries_entry_type_check;
ALTER TABLE public.ledger_entries
    ADD CONSTRAINT ck_ledger_entries_balance_math CHECK (
        ((entry_type = ANY (ARRAY['credit'::text, 'refund'::text, 'adjustment_credit'::text])) AND (balance_after = (balance_before + amount)))
        OR ((entry_type = ANY (ARRAY['debit'::text, 'adjustment_debit'::text])) AND (balance_after = (balance_before - amount)))
    );
ALTER TABLE public.ledger_entries
    ADD CONSTRAINT ledger_entries_entry_type_check CHECK (
        entry_type = ANY (ARRAY['credit'::text, 'debit'::text, 'refund'::text, 'adjustment_credit'::text, 'adjustment_debit'::text])
    );

DROP TABLE IF EXISTS public.cdkey_redemptions;
DROP SEQUENCE IF EXISTS public.cdkey_redemptions_id_seq;
DROP TABLE IF EXISTS public.cdkeys;
DROP SEQUENCE IF EXISTS public.cdkeys_id_seq;
