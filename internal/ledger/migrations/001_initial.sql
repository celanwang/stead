CREATE TABLE IF NOT EXISTS delegations (
    id VARBINARY(128) PRIMARY KEY,
    version BIGINT UNSIGNED NOT NULL,
    ready BOOLEAN NOT NULL,
    done BOOLEAN NOT NULL,
    wake_at DATETIME(6) NOT NULL,
    data JSON NOT NULL,
    KEY wake_schedule (done, wake_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS events (
    seq BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    id VARBINARY(191) NOT NULL UNIQUE,
    delegation_id VARBINARY(128) NOT NULL,
    source VARBINARY(191) NOT NULL,
    external_id VARBINARY(191) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    processed BOOLEAN NOT NULL,
    blocks_claim BOOLEAN NOT NULL,
    data JSON NOT NULL,
    UNIQUE KEY source_event (source, external_id),
    KEY event_queue (delegation_id, processed, seq),
    KEY claim_holds (delegation_id, processed, blocks_claim),
    FOREIGN KEY (delegation_id) REFERENCES delegations(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS grants (
    id VARBINARY(191) PRIMARY KEY,
    delegation_id VARBINARY(128) NOT NULL,
    data JSON NOT NULL,
    FOREIGN KEY (delegation_id) REFERENCES delegations(id),
    FOREIGN KEY (id) REFERENCES events(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS actions (
    seq BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    id VARBINARY(191) NOT NULL UNIQUE,
    delegation_id VARBINARY(128) NOT NULL,
    grant_id VARBINARY(191) NOT NULL,
    state VARCHAR(16) NOT NULL,
    data JSON NOT NULL,
    KEY outbox (state, seq),
    KEY delegation_actions (delegation_id, seq),
    FOREIGN KEY (delegation_id) REFERENCES delegations(id),
    FOREIGN KEY (grant_id) REFERENCES grants(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS resource_keys (
    owner VARBINARY(191) NOT NULL,
    resource_key VARBINARY(191) NOT NULL,
    PRIMARY KEY (owner, resource_key)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS occupations (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner VARBINARY(191) NOT NULL,
    resource_key VARBINARY(191) NOT NULL,
    action_id VARBINARY(191) NOT NULL,
    starts_at DATETIME(6) NOT NULL,
    ends_at DATETIME(6) NOT NULL,
    confirmed_at DATETIME(6),
    released_at DATETIME(6),
    KEY overlapping (owner, resource_key, released_at, starts_at),
    FOREIGN KEY (owner, resource_key) REFERENCES resource_keys(owner, resource_key),
    FOREIGN KEY (action_id) REFERENCES actions(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS audit_records (
    seq BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    delegation_id VARBINARY(128) NOT NULL,
    data JSON NOT NULL,
    KEY audit_history (delegation_id, seq),
    FOREIGN KEY (delegation_id) REFERENCES delegations(id)
) ENGINE=InnoDB;
