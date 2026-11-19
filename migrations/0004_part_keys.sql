ALTER TABLE upload_parts ADD COLUMN IF NOT EXISTS storage_key TEXT;
UPDATE upload_parts SET storage_key='uploads/'||session_id::text||'/'||part_no::text WHERE storage_key IS NULL;
ALTER TABLE upload_parts ALTER COLUMN storage_key SET NOT NULL;
