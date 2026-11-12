-- B01 owns safe settings access after B00; never SELECT credential ciphertext for browser DTOs.
-- name: GetSettings :one
SELECT * FROM settings WHERE id = 1;
