BEGIN;

-- Collection devices (shared by farmers, bound to a plot)
CREATE TABLE IF NOT EXISTS device (
    id BIGSERIAL PRIMARY KEY,
    device_code VARCHAR(64) NOT NULL,
    plot_id BIGINT NOT NULL REFERENCES plot(id),
    operator VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- device_code is the stable hardware identity: reinstall / operator change
-- re-register with the same code and keep the upload progress.
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_code ON device(device_code);
CREATE INDEX IF NOT EXISTS idx_device_plot ON device(plot_id);

-- Server-side ledger of received (device, seq): the basis for resume &
-- reconciliation. Rejected seqs are also recorded (activity_id NULL) so
-- reconciliation does not mistake them for lost records.
CREATE TABLE IF NOT EXISTS device_upload (
    id BIGSERIAL PRIMARY KEY,
    device_id BIGINT NOT NULL REFERENCES device(id),
    seq BIGINT NOT NULL,
    client_uuid VARCHAR(64) NOT NULL,
    activity_id BIGINT REFERENCES activity(id),
    status VARCHAR(16) NOT NULL CHECK (status IN ('accepted','rejected')),
    reason VARCHAR(255),
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_device_upload_device_seq ON device_upload(device_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_upload_uuid ON device_upload(client_uuid);

COMMIT;
