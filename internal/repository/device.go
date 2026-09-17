package repository

import (
	"cc-052/internal/model"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

// ErrNotFound 通用未找到
var ErrNotFound = errors.New("not found")

type DeviceRepo struct {
	db *sqlx.DB
}

func NewDeviceRepo(db *sqlx.DB) *DeviceRepo {
	return &DeviceRepo{db: db}
}

// ---------- 设备登记 ----------

func (r *DeviceRepo) Create(d *model.Device) error {
	query := `INSERT INTO device (serial_no, name, plot_id, operator, install_id, notes)
	          VALUES ($1, $2, $3, $4, $5, $6)
	          RETURNING id, status, created_at, updated_at`
	return r.db.QueryRow(query, d.SerialNo, d.Name, d.PlotID, d.Operator, d.InstallID, d.Notes).
		Scan(&d.ID, &d.Status, &d.CreatedAt, &d.UpdatedAt)
}

const deviceSelect = `SELECT id, serial_no, name, plot_id, operator, install_id, status,
                      last_synced_at, notes, created_at, updated_at FROM device`

func (r *DeviceRepo) GetByID(id int64) (*model.Device, error) {
	var d model.Device
	if err := r.db.Get(&d, deviceSelect+` WHERE id = $1`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepo) GetBySerialNo(serialNo string) (*model.Device, error) {
	var d model.Device
	if err := r.db.Get(&d, deviceSelect+` WHERE serial_no = $1`, serialNo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepo) List() ([]model.Device, error) {
	var devices []model.Device
	if err := r.db.Select(&devices, deviceSelect+` ORDER BY id`); err != nil {
		return nil, err
	}
	return devices, nil
}

// MarkReinstall 设备自报 install_id 变化（重装/换机后首次同步）：更新登记并留痕
func (r *DeviceRepo) MarkReinstall(deviceID int64, fromInstall, toInstall string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO device_handover (device_id, action, from_install_id, to_install_id, remark)
		 VALUES ($1, 'reinstall', $2, $3, '同步时检测到安装实例号变化，自动登记')`,
		deviceID, fromInstall, toInstall); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE device SET install_id = $2, updated_at = NOW() WHERE id = $1`,
		deviceID, toInstall); err != nil {
		return err
	}
	return tx.Commit()
}

// Handover 换人/换地/停用登记并留痕
func (r *DeviceRepo) Handover(d *model.Device, req *model.HandoverRequest) (*model.DeviceHandover, error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	h := &model.DeviceHandover{
		DeviceID:      d.ID,
		Action:        req.Action,
		FromPlotID:    d.PlotID,
		FromOperator:  d.Operator,
		FromInstallID: d.InstallID,
		ToPlotID:      d.PlotID,
		ToOperator:    d.Operator,
		ToInstallID:   d.InstallID,
		Remark:        req.Remark,
	}

	switch req.Action {
	case "handover":
		if req.ToPlotID != nil {
			h.ToPlotID = req.ToPlotID
		}
		if req.ToOperator != "" {
			h.ToOperator = req.ToOperator
		}
		_, err = tx.Exec(
			`UPDATE device SET plot_id = $2, operator = $3, updated_at = NOW() WHERE id = $1`,
			d.ID, h.ToPlotID, h.ToOperator)
	case "reinstall":
		h.ToInstallID = req.NewInstallID
		_, err = tx.Exec(
			`UPDATE device SET install_id = $2, updated_at = NOW() WHERE id = $1`,
			d.ID, h.ToInstallID)
	case "retire":
		_, err = tx.Exec(`UPDATE device SET status = 'retired', updated_at = NOW() WHERE id = $1`, d.ID)
	}
	if err != nil {
		return nil, err
	}

	if err := tx.QueryRow(
		`INSERT INTO device_handover
		 (device_id, action, from_plot_id, to_plot_id, from_operator, to_operator, from_install_id, to_install_id, remark)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, created_at`,
		h.DeviceID, h.Action, h.FromPlotID, h.ToPlotID,
		h.FromOperator, h.ToOperator, h.FromInstallID, h.ToInstallID, h.Remark).
		Scan(&h.ID, &h.CreatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return h, nil
}

func (r *DeviceRepo) ListHandovers(deviceID int64) ([]model.DeviceHandover, error) {
	var hs []model.DeviceHandover
	query := `SELECT id, device_id, action, from_plot_id, to_plot_id, from_operator, to_operator,
	          from_install_id, to_install_id, remark, created_at
	          FROM device_handover WHERE device_id = $1 ORDER BY id DESC`
	if err := r.db.Select(&hs, query, deviceID); err != nil {
		return nil, err
	}
	return hs, nil
}

// ---------- 游标 ----------

func (r *DeviceRepo) GetCursor(deviceID int64, installID string) (*model.DeviceSyncCursor, error) {
	var c model.DeviceSyncCursor
	err := r.db.Get(&c,
		`SELECT id, device_id, install_id, last_seq, acked_seq, client_sent_count,
		        server_accepted_count, updated_at
		 FROM device_sync_cursor WHERE device_id = $1 AND install_id = $2`,
		deviceID, installID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // 该安装实例还没同步过
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ActiveBatchIDOfPlot 设备当前挂载地块上最近的种植批次（item 未带 batch_id 时用）
func (r *DeviceRepo) ActiveBatchIDOfPlot(plotID int64) (int64, error) {
	var id int64
	err := r.db.Get(&id,
		`SELECT id FROM crop_batch WHERE plot_id = $1 ORDER BY id DESC LIMIT 1`, plotID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// ---------- 同步事务 ----------

// ItemDecision 是 service 对每条 item 同步前的业务判定（批次归属 + 校验结论）
type ItemDecision struct {
	BatchID      int64
	RejectReason string // 空串表示校验通过
}

// SyncTx 在一个事务内完成：游标行加锁串行化 → 逐条台账去重/落库 → 推进游标 → 算缺口。
// 去重三道闸：
//  1. 同安装实例同序号已处理过 → 幂等回放上次结论；
//  2. client_uuid 已在 activity 落过库（含换人/重装前）→ duplicate，不再落库；
//  3. activity.client_uuid 唯一索引最终兜底。
func (r *DeviceRepo) SyncTx(
	deviceID int64,
	installID string,
	clientLastSeq int64,
	clientSentCount int64,
	items []model.SyncItem,
	decisions []ItemDecision,
) (*model.SyncResponse, error) {

	resp := &model.SyncResponse{InstallID: installID, Received: len(items)}

	tx, err := r.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 锁住本设备本安装实例游标行（不存在则建），把并发同步串行化
	var cursorID int64
	var curAckedSeq int64
	if err := tx.QueryRow(
		`INSERT INTO device_sync_cursor (device_id, install_id) VALUES ($1, $2)
		 ON CONFLICT (device_id, install_id)
		 DO UPDATE SET updated_at = device_sync_cursor.updated_at
		 RETURNING id, acked_seq`, deviceID, installID).
		Scan(&cursorID, &curAckedSeq); err != nil {
		return nil, err
	}

	// 本安装实例已有台账：seq -> 记录（用于幂等回放与缺口计算）
	existingSeqs := map[int64]model.UploadRecord{}
	var rows []model.UploadRecord
	if err := tx.Select(&rows, ledgerSelect+`
		 WHERE device_id = $1 AND install_id = $2`, deviceID, installID); err != nil {
		return nil, err
	}
	for i := range rows {
		existingSeqs[rows[i].Seq] = rows[i]
	}

	// 本设备历史上已落库的 client_uuid（跨安装实例，重装/换人后重发去重的依据）
	knownUUIDs := map[string]bool{}
	var uuids []string
	if err := tx.Select(&uuids,
		`SELECT DISTINCT client_uuid FROM device_upload_record
		 WHERE device_id = $1 AND status IN ('accepted','duplicate')`, deviceID); err != nil {
		return nil, err
	}
	for _, u := range uuids {
		knownUUIDs[u] = true
	}

	maxSeq := clientLastSeq
	for i, item := range items {
		dec := decisions[i]
		if item.Seq > maxSeq {
			maxSeq = item.Seq
		}
		result := model.SyncItemResult{Seq: item.Seq, ClientUUID: item.ClientUUID, BatchID: dec.BatchID}

		// 1) 同序号处理过：幂等回放（同一批 payload 里的重号也走这里）
		if rec, ok := existingSeqs[item.Seq]; ok {
			fillResultFromRecord(&result, rec)
			resp.Results = append(resp.Results, result)
			bumpCount(resp, result.Status)
			continue
		}

		// 2) uuid 在任何安装实例已落过库（换人经手/设备重装后整段重发）：记 duplicate 台账，不重复落 activity
		if knownUUIDs[item.ClientUUID] {
			// 原记录的 activity 与批次是权威归属，不用当前设备的地块重新解析
			var orig struct {
				BatchID    *int64 `db:"batch_id"`
				ActivityID *int64 `db:"id"`
			}
			_ = tx.Get(&orig, `SELECT id, batch_id FROM activity WHERE client_uuid = $1`, item.ClientUUID)
			if err := insertLedger(tx, model.UploadRecord{
				DeviceID: deviceID, InstallID: installID, Seq: item.Seq,
				BatchID: orig.BatchID, ClientUUID: item.ClientUUID,
				Status: "duplicate", ActivityID: orig.ActivityID,
				RejectReason: "该记录以前已传过（换人/重装后重发），不重复落库",
			}); err != nil {
				return nil, err
			}
			result.Status = "duplicate"
			result.ActivityID = derefInt64(orig.ActivityID)
			result.BatchID = derefInt64(orig.BatchID)
			recordLedgerInMap(existingSeqs, item.Seq, result)
			resp.Results = append(resp.Results, result)
			resp.Duplicate++
			continue
		}

		// 3) 业务校验不通过：记 rejected 台账，不写 activity
		if dec.RejectReason != "" {
			if err := insertLedger(tx, model.UploadRecord{
				DeviceID: deviceID, InstallID: installID, Seq: item.Seq,
				BatchID: batchIDPtr(dec.BatchID), ClientUUID: item.ClientUUID,
				Status: "rejected", RejectReason: dec.RejectReason,
			}); err != nil {
				return nil, err
			}
			result.Status = "rejected"
			result.RejectReason = dec.RejectReason
			recordLedgerInMap(existingSeqs, item.Seq, result)
			resp.Results = append(resp.Results, result)
			resp.Rejected++
			continue
		}

		// 4) 新记录落库（activity.client_uuid 唯一索引最终兜底），再记 accepted 台账
		// 校验通过的记录一定解析出了批次
		if dec.BatchID == 0 {
			return nil, errors.New("internal: validated item has no batch")
		}
		activityID, duplicated, err := insertActivity(tx, dec.BatchID, &item.CreateActivityRequest)
		if err != nil {
			return nil, err
		}
		status := "accepted"
		reason := ""
		if duplicated {
			status = "duplicate"
			reason = "该记录以前已传过（换人/重装后重发），不重复落库"
			knownUUIDs[item.ClientUUID] = true
		}
		var actPtr *int64
		if activityID != 0 {
			id := activityID
			actPtr = &id
		}
		if err := insertLedger(tx, model.UploadRecord{
			DeviceID:     deviceID,
			InstallID:    installID,
			Seq:          item.Seq,
			BatchID:      batchIDPtr(dec.BatchID),
			ClientUUID:   item.ClientUUID,
			Status:       status,
			ActivityID:   actPtr,
			RejectReason: reason,
		}); err != nil {
			return nil, err
		}
		result.Status = status
		result.ActivityID = activityID
		result.RejectReason = reason
		recordLedgerInMap(existingSeqs, item.Seq, result)
		if duplicated {
			resp.Duplicate++
		} else {
			knownUUIDs[item.ClientUUID] = true
			resp.Accepted++
		}
		resp.Results = append(resp.Results, result)
	}

	// 服务端确认的连续前缀 1..N（台账中存在，含 accepted/duplicate/rejected 都算"服务端收到过"）
	acked, err := computeAckedSeq(tx, deviceID, installID, maxSeq)
	if err != nil {
		return nil, err
	}
	if acked < curAckedSeq {
		acked = curAckedSeq // 游标不回退
	}

	// 缺口：设备自称已送出（<= 设备游标）但台账没有的序号，一条条列出
	missing, err := missingSeqs(tx, deviceID, installID, maxSeq, 1000)
	if err != nil {
		return nil, err
	}

	var acceptedTotal int64
	if err := tx.Get(&acceptedTotal,
		`SELECT COUNT(*) FROM device_upload_record
		 WHERE device_id = $1 AND install_id = $2 AND status = 'accepted'`,
		deviceID, installID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`UPDATE device_sync_cursor
		 SET last_seq = $2, acked_seq = $3, client_sent_count = $4,
		     server_accepted_count = $5, updated_at = NOW()
		 WHERE id = $1`,
		cursorID, maxSeq, acked, clientSentCount, acceptedTotal); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE device SET last_synced_at = NOW() WHERE id = $1`, deviceID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	resp.ServerAckedSeq = acked
	resp.MissingSeqs = missing
	resp.ClientSentCount = clientSentCount
	resp.ServerConfirmedCount = acceptedTotal
	return resp, nil
}

const ledgerSelect = `SELECT id, device_id, install_id, seq, batch_id, client_uuid, status,
                      activity_id, reject_reason, created_at
                      FROM device_upload_record`

func insertLedger(tx *sqlx.Tx, rec model.UploadRecord) error {
	_, err := tx.Exec(
		`INSERT INTO device_upload_record
		 (device_id, install_id, seq, batch_id, client_uuid, status, activity_id, reject_reason)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (device_id, install_id, seq) DO NOTHING`,
		rec.DeviceID, rec.InstallID, rec.Seq, rec.BatchID, rec.ClientUUID,
		rec.Status, rec.ActivityID, rec.RejectReason)
	return err
}

// insertActivity 插入农事记录；返回 activityID 与是否因 client_uuid 冲突而重复
func insertActivity(tx *sqlx.Tx, batchID int64, req *model.CreateActivityRequest) (int64, bool, error) {
	happenedAt, err := ParseHappenedAt(req.HappenedAt)
	if err != nil {
		return 0, false, err
	}
	var id int64
	err = tx.QueryRow(
		`INSERT INTO activity (batch_id, client_uuid, kind, happened_at, input_id, dose, dose_unit, operator, photos, geo)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (client_uuid) DO NOTHING RETURNING id`,
		batchID, req.ClientUUID, req.Kind, happenedAt,
		req.InputID, req.Dose, req.DoseUnit, req.Operator, req.Photos, req.Geo).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if e := tx.Get(&id, `SELECT id FROM activity WHERE client_uuid = $1`, req.ClientUUID); e != nil {
			return 0, false, e
		}
		return id, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, false, nil
}

// sqlxExt *sqlx.DB 与 *sqlx.Tx 共同满足的最小接口
type sqlxExt interface {
	QueryRowx(query string, args ...interface{}) *sqlx.Row
	Select(dest interface{}, query string, args ...interface{}) error
}

func computeAckedSeq(q sqlxExt, deviceID int64, installID string, upper int64) (int64, error) {
	if upper <= 0 {
		return 0, nil
	}
	var acked int64
	err := q.QueryRowx(`
		SELECT COALESCE(MAX(s.seq), 0) FROM (
			SELECT seq, ROW_NUMBER() OVER (ORDER BY seq) AS rn
			FROM device_upload_record
			WHERE device_id = $1 AND install_id = $2 AND seq BETWEEN 1 AND $3
		) s WHERE s.seq = s.rn`,
		deviceID, installID, upper).Scan(&acked)
	return acked, err
}

func missingSeqs(q sqlxExt, deviceID int64, installID string, upper int64, limit int) ([]int64, error) {
	if upper <= 0 {
		return []int64{}, nil
	}
	var seqs []int64
	err := q.Select(&seqs, `
		SELECT s.seq FROM generate_series(1, $3) AS s(seq)
		WHERE NOT EXISTS (
			SELECT 1 FROM device_upload_record r
			WHERE r.device_id = $1 AND r.install_id = $2 AND r.seq = s.seq
		)
		ORDER BY s.seq LIMIT $4`,
		deviceID, installID, upper, limit)
	return seqs, err
}

// ---------- 进度核对 ----------

func (r *DeviceRepo) CountByStatus(deviceID int64, installID string) (accepted, duplicate, rejected, total int64, err error) {
	rows := []struct {
		Status string
		N      int64
	}{}
	if e := r.db.Select(&rows,
		`SELECT status, COUNT(*) AS n FROM device_upload_record
		 WHERE device_id = $1 AND install_id = $2 GROUP BY status`,
		deviceID, installID); e != nil {
		return 0, 0, 0, 0, e
	}
	for _, row := range rows {
		total += row.N
		switch row.Status {
		case "accepted":
			accepted = row.N
		case "duplicate":
			duplicate = row.N
		case "rejected":
			rejected = row.N
		}
	}
	return
}

// MissingSeqsUpTo 设备自称已送出 upper 条、服务端却没有台账的序号
func (r *DeviceRepo) MissingSeqsUpTo(deviceID int64, installID string, upper int64) ([]int64, error) {
	return missingSeqs(r.db, deviceID, installID, upper, 1000)
}

func (r *DeviceRepo) ListRecords(deviceID int64, installID string, limit, offset int) ([]model.UploadRecord, error) {
	var rows []model.UploadRecord
	if err := r.db.Select(&rows, ledgerSelect+`
		 WHERE device_id = $1 AND install_id = $2
		 ORDER BY seq ASC LIMIT $3 OFFSET $4`,
		deviceID, installID, limit, offset); err != nil {
		return nil, err
	}
	return rows, nil
}

// ---------- helpers ----------

// ParseHappenedAt 兼容 RFC3339 与纯日期
func ParseHappenedAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

func bumpCount(resp *model.SyncResponse, status string) {
	switch status {
	case "accepted":
		resp.Accepted++
	case "duplicate":
		resp.Duplicate++
	case "rejected":
		resp.Rejected++
	}
}

func fillResultFromRecord(result *model.SyncItemResult, rec model.UploadRecord) {
	result.Status = rec.Status
	result.BatchID = derefInt64(rec.BatchID)
	result.ActivityID = derefInt64(rec.ActivityID)
	result.RejectReason = rec.RejectReason
}

func batchIDPtr(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func recordLedgerInMap(m map[int64]model.UploadRecord, seq int64, result model.SyncItemResult) {
	rec := model.UploadRecord{
		Seq: seq, BatchID: batchIDPtr(result.BatchID), ClientUUID: result.ClientUUID,
		Status: result.Status, RejectReason: result.RejectReason,
	}
	if result.ActivityID != 0 {
		id := result.ActivityID
		rec.ActivityID = &id
	}
	m[seq] = rec
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
