-- Neutralize the /talk thread intent (issue #54): 'ask-sebastian' baked a
-- deployment-specific agent's personal name into a protocol identifier.
-- The canonical intent is now 'talk' (a capability name); rewriting the
-- existing rows here means readers never need a compat branch.
UPDATE chat_threads SET intent = 'talk' WHERE intent = 'ask-sebastian';
