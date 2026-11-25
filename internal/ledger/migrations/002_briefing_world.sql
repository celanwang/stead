CREATE TABLE IF NOT EXISTS briefing_facts (
    owner VARBINARY(191) NOT NULL,
    project VARBINARY(191) NOT NULL,
    id VARBINARY(191) NOT NULL,
    revision BIGINT UNSIGNED NOT NULL,
    shareable BOOLEAN NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (owner, project, id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS briefing_audiences (
    owner VARBINARY(191) NOT NULL,
    project VARBINARY(191) NOT NULL,
    target VARBINARY(191) NOT NULL,
    revision BIGINT UNSIGNED NOT NULL,
    class VARBINARY(191) NOT NULL,
    PRIMARY KEY (owner, project, target)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS briefing_deliveries (
    id VARBINARY(191) NOT NULL PRIMARY KEY,
    delegation_id VARBINARY(128) NOT NULL,
    effect_key VARBINARY(64) NOT NULL UNIQUE,
    target VARBINARY(191) NOT NULL,
    audience_revision BIGINT UNSIGNED NOT NULL,
    audience_class VARBINARY(191) NOT NULL,
    artifact_id VARBINARY(64) NOT NULL,
    content TEXT NOT NULL,
    manual BOOLEAN NOT NULL,
    delivered_at DATETIME(6) NOT NULL,
    KEY delegation_deliveries (delegation_id, delivered_at)
) ENGINE=InnoDB;
