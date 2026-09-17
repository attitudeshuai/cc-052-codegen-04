package service

import (
	"cc-052/internal/model"
	"cc-052/internal/repository"
	"errors"
	"fmt"
)

type DeviceService struct {
	repo      *repository.DeviceRepo
	batchRepo *repository.BatchRepo
}

func NewDeviceService(repo *repository.DeviceRepo, batchRepo *repository.BatchRepo) *DeviceService {
	return &DeviceService{repo: repo, batchRepo: batchRepo}
}

var (
	ErrDeviceNotFound  = errors.New("设备未登记")
	ErrDeviceRetired   = errors.New("设备已停用")
	ErrSerialDuplicate = errors.New("设备编号已登记")
	ErrNoBatch         = errors.New("设备当前未挂在有种植批次的地块上，记录无法归属批次")
)

var validKinds = map[model.ActivityKind]bool{
	model.ActivityFertilize:  true,
	model.ActivityPesticide:  true,
	model.ActivityIrrigation: true,
	model.ActivityWeed:       true,
}

// Register 新设备登记
func (s *DeviceService) Register(req *model.CreateDeviceRequest) (*model.Device, error) {
	if _, err := s.repo.GetBySerialNo(req.SerialNo); err == nil {
		return nil, ErrSerialDuplicate
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	d := &model.Device{
		SerialNo:  req.SerialNo,
		Name:      req.Name,
		PlotID:    req.PlotID,
		Operator:  req.Operator,
		InstallID: req.InstallID,
		Notes:     req.Notes,
	}
	if err := s.repo.Create(d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *DeviceService) Get(id int64) (*model.Device, error) {
	return s.repo.GetByID(id)
}

func (s *DeviceService) List() ([]model.Device, error) {
	return s.repo.List()
}

// Handover 换人/换地/重装/停用
func (s *DeviceService) Handover(id int64, req *model.HandoverRequest) (*model.DeviceHandover, error) {
	d, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if req.Action == "reinstall" && req.NewInstallID == "" {
		req.NewInstallID = fmt.Sprintf("inst-%d-%d", d.ID, d.CreatedAt.UnixNano())
	}
	h, err := s.repo.Handover(d, req)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *DeviceService) ListHandovers(id int64) ([]model.DeviceHandover, error) {
	if _, err := s.repo.GetByID(id); err != nil {
		return nil, err
	}
	return s.repo.ListHandovers(id)
}

// Sync 设备一次上报同步：续传游标、逐条核对、算缺口
func (s *DeviceService) Sync(serialNo string, req *model.SyncRequest) (*model.SyncResponse, error) {
	device, err := s.repo.GetBySerialNo(serialNo)
	if err != nil {
		return nil, ErrDeviceNotFound
	}
	if device.Status == "retired" {
		return nil, ErrDeviceRetired
	}

	// 设备自报的安装实例号与登记不一致且非空：视为重装过，自动登记留痕后按新实例走
	installID := device.InstallID
	if req.InstallID != "" && req.InstallID != device.InstallID {
		if err := s.repo.MarkReinstall(device.ID, device.InstallID, req.InstallID); err != nil {
			return nil, err
		}
		installID = req.InstallID
		device.InstallID = installID
	}

	// 同步前逐条做业务判定：批次归属 + 时序/格式校验
	decisions := make([]repository.ItemDecision, len(req.Items))
	for i, item := range req.Items {
		dec := repository.ItemDecision{}

		batchID, reason := s.resolveBatch(device, item)
		dec.BatchID = batchID
		if reason != "" {
			dec.RejectReason = reason
			decisions[i] = dec
			continue
		}

		if reason = s.validateItem(item, batchID); reason != "" {
			dec.RejectReason = reason
		}
		decisions[i] = dec
	}

	return s.repo.SyncTx(device.ID, installID, req.ClientLastSeq, req.ClientSentCount, req.Items, decisions)
}

// resolveBatch item 自带 batch_id 用自带的，否则落到设备当前登记地块上最近的种植批次
func (s *DeviceService) resolveBatch(device *model.Device, item model.SyncItem) (int64, string) {
	if item.BatchID != nil {
		return *item.BatchID, ""
	}
	if device.PlotID == nil {
		return 0, "设备未登记挂载地块，且记录未带 batch_id"
	}
	id, err := s.repo.ActiveBatchIDOfPlot(*device.PlotID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return 0, ErrNoBatch.Error()
		}
		return 0, err.Error()
	}
	return id, ""
}

// validateItem 复用现有时序规则：不早于播种、不晚于采收；并校验类型与时间格式
func (s *DeviceService) validateItem(item model.SyncItem, batchID int64) string {
	if item.ClientUUID == "" {
		return "client_uuid 不能为空"
	}
	if !validKinds[item.Kind] {
		return "未知农事类型: " + string(item.Kind)
	}
	if item.Operator == "" {
		return "operator 不能为空"
	}
	happenedAt, err := repository.ParseHappenedAt(item.HappenedAt)
	if err != nil {
		return "happened_at 时间格式无法解析: " + item.HappenedAt
	}
	batch, err := s.batchRepo.GetByID(batchID)
	if err != nil {
		return "批次不存在: " + fmt.Sprint(batchID)
	}
	if happenedAt.Before(batch.SowingDate) {
		return "农事时间早于播种日期，时序不合法"
	}
	if batch.HarvestDate != nil && happenedAt.After(*batch.HarvestDate) {
		return "农事时间晚于采收日期，时序不合法"
	}
	return ""
}

// Progress 上报进度核对报告
func (s *DeviceService) Progress(id int64, installID string) (*model.ProgressReport, error) {
	device, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if installID == "" {
		installID = device.InstallID
	}

	report := &model.ProgressReport{Device: *device, MissingSeqs: []int64{}}

	cursor, err := s.repo.GetCursor(id, installID)
	if err != nil {
		return nil, err
	}
	report.Cursor = cursor

	accepted, duplicate, rejected, total, err := s.repo.CountByStatus(id, installID)
	if err != nil {
		return nil, err
	}
	report.AcceptedCount = accepted
	report.DuplicateCount = duplicate
	report.RejectedCount = rejected
	report.LedgerCount = total

	// 以设备游标声称的位置为上界找缺口；游标还没建则没有可核对的范围
	if cursor != nil && cursor.LastSeq > 0 {
		missing, err := s.repo.MissingSeqsUpTo(id, installID, cursor.LastSeq)
		if err != nil {
			return nil, err
		}
		report.MissingSeqs = missing
	}

	// 自称条数 vs 服务端实际台账条数，且不存在缺口，才算对上
	clientClaims := int64(0)
	if cursor != nil {
		clientClaims = cursor.ClientSentCount
	}
	report.Consistent = len(report.MissingSeqs) == 0 && clientClaims == total
	return report, nil
}

// Records 上报表台账明细分页（逐条核对用）
func (s *DeviceService) Records(id int64, installID string, limit, offset int) ([]model.UploadRecord, error) {
	if installID == "" {
		device, err := s.repo.GetByID(id)
		if err != nil {
			return nil, err
		}
		installID = device.InstallID
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.repo.ListRecords(id, installID, limit, offset)
}
