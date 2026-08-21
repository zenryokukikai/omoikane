-- Personal librarian icon (issue #85): a short text icon (emoji) and/or
-- an uploaded image. The uploaded image wins when both are set; empty
-- icon + no image falls back to the built-in 🤖 at render time.
--
-- The image lives in the DB as a blob: icons are small (≤1MB enforced
-- at upload), one per user, and blob-in-DB avoids a second storage
-- contract (disk paths, volumes, backup) for a single tiny file.
-- icon_ver increments on every image change and busts the browser cache
-- via the ?v= query on the serving route.
--
-- NOTE: shipped migration version numbers are never reusable.
ALTER TABLE user_librarians ADD COLUMN icon TEXT NOT NULL DEFAULT '';
ALTER TABLE user_librarians ADD COLUMN icon_image BLOB;
ALTER TABLE user_librarians ADD COLUMN icon_mime TEXT NOT NULL DEFAULT '';
ALTER TABLE user_librarians ADD COLUMN icon_ver INTEGER NOT NULL DEFAULT 0;
