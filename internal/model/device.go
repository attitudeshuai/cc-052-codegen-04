package model

import "time"

// Device 采集设备登记
type Device struct {
	ID           int64      `db:"id" json:"id"`
	SerialNo     string     `db:"serial_no" json:"serial_no"`
	Name         string     `db:"name" json:"name"`
	PlotID       *int64     `db:"plot_id" json:"plot_id,omitempty"`
	Operator     string     `db:"operator" json:"operator"`
	InstallID    string     `db:"install_id" json:"install_id"`
	Status       string     `db:"status" json:"status"`
	LastSyncedAt *time.Time `db:"last_synced_at" json:"last_synced_at,omitempty"`
	Notes        string     `db:"notes" json:"notes"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
}

type CreateDeviceRequest struct {
	SerialNo  string `json:"serial_no" binding:"required"`
	Name      string `json:"name"`
	PlotID    *int64 `json:"plot_id"`
	Operator  string `json:"operator"`
	InstallID string `json:"install_id"`
	Notes     string `json:"notes"`
}

// HandoverRequest 设备换人/换地/重装登记
type HandoverRequest struct {
	Action     string `json:"action" binding:"required,oneof=handover reinstall retire"`
	ToPlotID   *int64 `json:"to_plot_id"`
	ToOperator string `json:"to_operator"`
	// NewInstallID 重装时新安装实例号；不传则由服务端生成
	NewInstallID string `json:"new_install_id"`
	Remark       string `json:"remark"`
}

type DeviceHandover struct {
	ID            int64     `db:"id" json:"id"`
	DeviceID      int64     `db:"device_id" json:"device_id"`
	Action        string    `db:"action" json:"action"`
	FromPlotID    *int64    `db:"from_plot_id" json:"from_plot_id,omitempty"`
	ToPlotID      *int64    `db:"to_plot_id" json:"to_plot_id,omitempty"`
	FromOperator  string    `db:"from_operator" json:"from_operator"`
	ToOperator    string    `db:"to_operator" json:"to_operator"`
	FromInstallID string    `db:"from_install_id" json:"from_install_id"`
	ToInstallID   string    `db:"to_install_id" json:"to_install_id"`
	Remark        string    `db:"remark" json:"remark"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

// DeviceSyncCursor 上报进度游标
type DeviceSyncCursor struct {
	ID                  int64     `db:"id" json:"id"`
	DeviceID            int64     `db:"device_id" json:"device_id"`
	InstallID           string    `db:"install_id" json:"install_id"`
	LastSeq             int64     `db:"last_seq" json:"last_seq"`
	AckedSeq            int64     `db:"acked_seq" json:"acked_seq"`
	ClientSentCount     int64     `db:"client_sent_count" json:"client_sent_count"`
	ServerAcceptedCount int64     `db:"server_accepted_count" json:"server_accepted_count"`
	UpdatedAt           time.Time `db:"updated_at" json:"updated_at"`
}

// SyncItem 设备上报的一条记录（序号 + 农事记录内容）
type SyncItem struct {
	Seq int64 `json:"seq" binding:"required"`
	// BatchID 可选；不传则挂到设备当前登记的地块在种养批次
	BatchID *int64 `json:"batch_id"`
	CreateActivityRequest
}

// SyncRequest 设备一次同步请求
type SyncRequest struct {
	// 设备自报的安装实例号；与登记不一致时按"重装过"处理而不是拒绝
	InstallID string `json:"install_id"`
	// 设备自报本次同步前已送出的最大序号（游标），用于与服务端核对
	ClientLastSeq int64 `json:"client_last_seq"`
	// 设备自报累计送出条数
	ClientSentCount int64      `json:"client_sent_count"`
	Items           []SyncItem `json:"items" binding:"required,min=1,dive"`
}

// SyncItemResult 每条上报的核对结果
type SyncItemResult struct {
	Seq          int64  `json:"seq"`
	ClientUUID   string `json:"client_uuid"`
	BatchID      int64  `json:"batch_id"`
	Status       string `json:"status"` // accepted | duplicate | rejected
	ActivityID   int64  `json:"activity_id,omitempty"`
	RejectReason string `json:"reject_reason,omitempty"`
}

// SyncResponse 同步结果：设备据此把游标推进到 server_acked_seq
type SyncResponse struct {
	InstallID string `json:"install_id"`
	Received  int    `json:"received"`
	Accepted  int    `json:"accepted"`
	Duplicate int    `json:"duplicate"`
	Rejected  int    `json:"rejected"`
	// 服务端确认的连续游标：1..N 都已有台账记录，下次从 N+1 开始传
	ServerAckedSeq int64 `json:"server_acked_seq"`
	// 缺口：设备声称已送出（<=client_last_seq）但服务端没有台账的序号，一条条列出
	MissingSeqs []int64 `json:"missing_seqs"`
	// 设备自称已送出条数与服务端实际确认条数对不上时给出差异
	ClientSentCount      int64            `json:"client_sent_count"`
	ServerConfirmedCount int64            `json:"server_confirmed_count"`
	Results              []SyncItemResult `json:"results"`
}

// UploadRecord 上报台账
type UploadRecord struct {
	ID           int64     `db:"id" json:"id"`
	DeviceID     int64     `db:"device_id" json:"device_id"`
	InstallID    string    `db:"install_id" json:"install_id"`
	Seq          int64     `db:"seq" json:"seq"`
	BatchID      *int64    `db:"batch_id" json:"batch_id,omitempty"`
	ClientUUID   string    `db:"client_uuid" json:"client_uuid"`
	Status       string    `db:"status" json:"status"`
	ActivityID   *int64    `db:"activity_id" json:"activity_id,omitempty"`
	RejectReason string    `db:"reject_reason" json:"reject_reason"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

// ProgressReport 设备上报进度核对报告
type ProgressReport struct {
	Device Device            `json:"device"`
	Cursor *DeviceSyncCursor `json:"cursor,omitempty"`
	// 实际落库（accepted）的去重条数
	AcceptedCount int64 `json:"accepted_count"`
	// 台账总条数（含重复/被拒）
	LedgerCount    int64 `json:"ledger_count"`
	DuplicateCount int64 `json:"duplicate_count"`
	RejectedCount  int64 `json:"rejected_count"`
	// 缺口序号：1..client_last_seq 中服务端无台账的
	MissingSeqs []int64 `json:"missing_seqs"`
	// 设备自称送出条数与服务端确认条数是否一致
	Consistent bool `json:"consistent"`
}
