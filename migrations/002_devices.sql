BEGIN;

-- 采集设备登记：十几个农户共用几台设备轮流上报
CREATE TABLE IF NOT EXISTS device (
    id BIGSERIAL PRIMARY KEY,
    -- 设备机身编号（设备端自报，唯一），重装系统也不变
    serial_no VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL DEFAULT '',
    -- 当前挂载的地块（可空，表示设备在库/未下地）
    plot_id BIGINT REFERENCES plot(id),
    -- 当前经手农户
    operator VARCHAR(128) NOT NULL DEFAULT '',
    -- 设备端最近一次重装/换机时生成的安装实例号；服务端据此识别"重装过"
    install_id VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active','retired')),
    last_synced_at TIMESTAMPTZ,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_device_serial_no ON device(serial_no);

-- 设备交接/换人/换地/重装 留痕
CREATE TABLE IF NOT EXISTS device_handover (
    id BIGSERIAL PRIMARY KEY,
    device_id BIGINT NOT NULL REFERENCES device(id),
    -- handover: 换人/换地；reinstall: 设备重装（install_id 变化）；retire: 停用
    action VARCHAR(16) NOT NULL CHECK (action IN ('handover','reinstall','retire')),
    from_plot_id BIGINT REFERENCES plot(id),
    to_plot_id BIGINT REFERENCES plot(id),
    from_operator VARCHAR(128) NOT NULL DEFAULT '',
    to_operator VARCHAR(128) NOT NULL DEFAULT '',
    from_install_id VARCHAR(64) NOT NULL DEFAULT '',
    to_install_id VARCHAR(64) NOT NULL DEFAULT '',
    remark TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_device_handover_device ON device_handover(device_id);

-- 设备同步游标：每台设备每个（重装）安装实例一条
-- 下次来同步"接着上次传到的地方往下走"；重装换新 install_id 从 1 重计，
-- 但服务端台账（device_upload_record + activity.client_uuid）还在，传过的不会重复落库
CREATE TABLE IF NOT EXISTS device_sync_cursor (
    id BIGSERIAL PRIMARY KEY,
    device_id BIGINT NOT NULL REFERENCES device(id),
    install_id VARCHAR(64) NOT NULL DEFAULT '',
    -- 设备自报的游标：本安装实例内已送出的最大序号
    last_seq BIGINT NOT NULL DEFAULT 0,
    -- 服务端确认的游标：已确认落库（含以前传过）的最大连续序号
    acked_seq BIGINT NOT NULL DEFAULT 0,
    -- 设备自报累计送出条数
    client_sent_count BIGINT NOT NULL DEFAULT 0,
    -- 服务端实际新落库条数（不含幂等跳过）
    server_accepted_count BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (device_id, install_id)
);

-- 上报表台账：设备(含安装实例、序号、batch)声称传过的每一条
-- 与 activity 双去重：client_uuid 全局唯一幂等；(device_id, install_id, seq) 防同实例重号
CREATE TABLE IF NOT EXISTS device_upload_record (
    id BIGSERIAL PRIMARY KEY,
    device_id BIGINT NOT NULL REFERENCES device(id),
    install_id VARCHAR(64) NOT NULL DEFAULT '',
    seq BIGINT NOT NULL,
    -- 记录无法归属批次（设备未挂地等）被拒时可能为空
    batch_id BIGINT REFERENCES crop_batch(id),
    client_uuid VARCHAR(64) NOT NULL,
    -- accepted: 新落库；duplicate: client_uuid 以前已传过（换人/重装重发，不重复落库）；
    -- rejected: 时序/校验不通过未入库
    status VARCHAR(16) NOT NULL CHECK (status IN ('accepted','duplicate','rejected')),
    activity_id BIGINT REFERENCES activity(id),
    reject_reason VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (device_id, install_id, seq)
);

-- client_uuid 普通索引而非唯一：重装换新 install_id 后整段重发，同一 uuid 会在
-- 新实例台账里再留一条 duplicate；真正的幂等去重由 activity.client_uuid 唯一索引兜底
CREATE INDEX IF NOT EXISTS idx_device_upload_uuid ON device_upload_record(client_uuid);
CREATE INDEX IF NOT EXISTS idx_device_upload_device ON device_upload_record(device_id, install_id, seq);
CREATE INDEX IF NOT EXISTS idx_device_upload_batch ON device_upload_record(batch_id);

COMMIT;
