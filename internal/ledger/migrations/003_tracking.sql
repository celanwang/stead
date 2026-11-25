CREATE TABLE IF NOT EXISTS tracked_items (
    owner VARBINARY(191) NOT NULL,
    project VARBINARY(191) NOT NULL,
    source VARBINARY(191) NOT NULL,
    object_id VARBINARY(191) NOT NULL,
    data JSON NOT NULL,
    PRIMARY KEY (owner, project, source, object_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS tracked_observations (
    seq BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner VARBINARY(191) NOT NULL,
    project VARBINARY(191) NOT NULL,
    source VARBINARY(191) NOT NULL,
    object_id VARBINARY(191) NOT NULL,
    event_id VARBINARY(191) NOT NULL,
    disposition VARCHAR(16) NOT NULL,
    read_at DATETIME(6),
    suppressed_at DATETIME(6),
    data JSON NOT NULL,
    UNIQUE KEY tracking_event (owner, project, source, event_id),
    KEY tracking_object (owner, project, source, object_id, seq),
    KEY tracking_feed (owner, project, read_at, suppressed_at, seq),
    FOREIGN KEY (owner, project, source, object_id)
        REFERENCES tracked_items(owner, project, source, object_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS agent_preferences (
    owner VARBINARY(191) NOT NULL,
    id VARBINARY(191) NOT NULL,
    name VARBINARY(191) NOT NULL,
    data JSON NOT NULL,
    PRIMARY KEY (owner, id),
    KEY preference_name (owner, name)
) ENGINE=InnoDB;
