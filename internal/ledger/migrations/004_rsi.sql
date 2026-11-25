CREATE TABLE IF NOT EXISTS rsi_projects (
    owner VARBINARY(191) NOT NULL,
    project VARBINARY(191) NOT NULL,
    revision BIGINT UNSIGNED NOT NULL,
    data JSON NOT NULL,
    PRIMARY KEY (owner, project)
) ENGINE=InnoDB;
