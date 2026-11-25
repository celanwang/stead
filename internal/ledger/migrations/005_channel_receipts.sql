CREATE TABLE IF NOT EXISTS channel_delivery_acknowledgments (
    action_id VARBINARY(191) PRIMARY KEY,
    provider VARBINARY(32) NOT NULL,
    message_id VARBINARY(191) NOT NULL,
    recorded_at DATETIME(6) NOT NULL,
    UNIQUE KEY provider_message (provider, message_id),
    FOREIGN KEY (action_id) REFERENCES actions(id)
) ENGINE=InnoDB;
