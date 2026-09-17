package model

import "time"

// Device statuses
const (
	DeviceStatusActive   = "active"
	DeviceStatusDisabled = "disabled"
)

// Ledger entry statuses (device_upload.status)
const (
	UploadAccepted = "accepted"
	UploadRejected = "rejected"
)

// Per-record sync receipt statuses
const (
	ReceiptInserted  = "inserted"  // newly stored
	ReceiptDuplicate = "duplicate" // already stored, idempotent hit
	ReceiptRejected  = "rejected"  // validation failed, recorded in ledger with reason
	ReceiptConflict  = "conflict"  // seq already used with a different client_uuid
)

type Device struct {
	ID         int64     `db:"id" json:"id"`
	DeviceCode string    `db:"device_code" json:"device_code"`
	PlotID     int64     `db:"plot_id" json:"plot_id"`
	Operator   string    `db:"operator" json:"operator"`
	Status     string    `db:"status" json:"status"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}

// DeviceView enriches Device with plot/farm names for registry listings.
type DeviceView struct {
	Device
	PlotName string `db:"plot_name" json:"plot_name"`
	FarmID   int64  `db:"farm_id" json:"farm_id"`
}

type DeviceUpload struct {
	ID         int64     `db:"id" json:"id"`
	DeviceID   int64     `db:"device_id" json:"device_id"`
	Seq        int64     `db:"seq" json:"seq"`
	ClientUUID string    `db:"client_uuid" json:"client_uuid"`
	ActivityID *int64    `db:"activity_id" json:"activity_id,omitempty"`
	Status     string    `db:"status" json:"status"`
	Reason     *string   `db:"reason" json:"reason,omitempty"`
	ReceivedAt time.Time `db:"received_at" json:"received_at"`
}

type RegisterDeviceRequest struct {
	DeviceCode string `json:"device_code" binding:"required"`
	PlotID     int64  `json:"plot_id" binding:"required"`
	Operator   string `json:"operator" binding:"required"`
}

type UpdateDeviceRequest struct {
	PlotID   *int64  `json:"plot_id"`
	Operator *string `json:"operator"`
	Status   *string `json:"status"`
}

// SyncRecordRequest is one record in a device sync batch. seq is the
// device-local monotonically increasing number (1,2,3,...); client_uuid is
// the globally unique idempotency key (same as activity.client_uuid).
type SyncRecordRequest struct {
	Seq        int64        `json:"seq" binding:"required,min=1"`
	ClientUUID string       `json:"client_uuid" binding:"required"`
	BatchID    int64        `json:"batch_id" binding:"required"`
	Kind       ActivityKind `json:"kind" binding:"required"`
	HappenedAt string       `json:"happened_at" binding:"required"`
	InputID    *int64       `json:"input_id"`
	Dose       *float64     `json:"dose"`
	DoseUnit   *string      `json:"dose_unit"`
	Operator   string       `json:"operator"` // optional, defaults to the device's registered operator
	Photos     StringMap    `json:"photos"`
	Geo        *string      `json:"geo"`
}

type SyncRecordsRequest struct {
	Records []SyncRecordRequest `json:"records" binding:"required,min=1,dive"`
}

// SyncReceipt is the per-record result of a sync call.
type SyncReceipt struct {
	Seq        int64  `json:"seq"`
	ClientUUID string `json:"client_uuid"`
	Status     string `json:"status"` // inserted | duplicate | rejected | conflict
	ActivityID *int64 `json:"activity_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type SyncResult struct {
	DeviceID   int64               `json:"device_id"`
	Inserted   int                 `json:"inserted"`
	Duplicated int                 `json:"duplicated"`
	Rejected   int                 `json:"rejected"`
	Conflicted int                 `json:"conflicted"`
	Receipts   []SyncReceipt       `json:"receipts"`
	Checkpoint *SyncStatusResponse `json:"checkpoint"`
}

// SyncStatusResponse is the checkpoint a device uses to resume: send the
// gaps first, then continue from next_seq.
type SyncStatusResponse struct {
	DeviceID         int64      `json:"device_id"`
	DeviceCode       string     `json:"device_code"`
	NextSeq          int64      `json:"next_seq"`
	MaxContiguousSeq int64      `json:"max_contiguous_seq"`
	MaxSeq           int64      `json:"max_seq"`
	TotalRecords     int        `json:"total_records"`
	Gaps             []int64    `json:"gaps"`
	LastReceivedAt   *time.Time `json:"last_received_at,omitempty"`
}

// ReconcileResponse answers "device claims it sent 1..claimed_max_seq":
// which seqs the server never received (missing) and which were rejected.
type ReconcileResponse struct {
	DeviceID         int64         `json:"device_id"`
	ClaimedMaxSeq    int64         `json:"claimed_max_seq"`
	Received         int           `json:"received"` // ledger rows with seq <= claimed_max_seq
	Accepted         int           `json:"accepted"`
	Rejected         []RejectedSeq `json:"rejected"`
	MissingCount     int           `json:"missing_count"`
	MissingSeqs      []int64       `json:"missing_seqs"` // the gaps, one by one
	MissingTruncated bool          `json:"missing_truncated"`
	ExtraBeyondClaim int           `json:"extra_beyond_claim"` // server holds seqs above the claim
}

type RejectedSeq struct {
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
}
