INSERT INTO users (id, full_name) VALUES
    (7, 'Ada Lovelace'),
    (8, 'Grace Hopper');
SELECT setval(pg_get_serial_sequence('users', 'id'), (SELECT max(id) FROM users));
