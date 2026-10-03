-- name: GetProcessedUpload :one
SELECT file_name, bytes_received, saved_as, wav_file, received_at
FROM processed_uploads
WHERE file_name = $1;

-- Returns no row when file_name is already taken: another request with the same name won the race.
-- name: InsertProcessedUpload :one
INSERT INTO processed_uploads (file_name, bytes_received, saved_as, wav_file)
VALUES ($1, $2, $3, $4)
ON CONFLICT (file_name) DO NOTHING
RETURNING file_name, bytes_received, saved_as, wav_file, received_at;
