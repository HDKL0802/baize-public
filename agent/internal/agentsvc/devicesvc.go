package agentsvc

import (
	"errors"
	"strings"
)

// SetDeviceRemark 给设备加/改备注；remark 传空串表示清除备注。
//
// 备注存在 config.json 的 deviceRemarks 里（key = 设备 id），不写进设备表：
// 它是"用户的标注"，不是设备上报的状态——设备重连、重装、换网络都不该丢。
func (s *Service) SetDeviceRemark(deviceID, remark string) error {
	id := strings.TrimSpace(deviceID)
	if id == "" {
		return errors.New("缺少设备 id")
	}
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	// 复制一份再改：直接改原 map 会影响正在被读的那份
	next := make(map[string]string, len(cfg.DeviceRemarks)+1)
	for k, v := range cfg.DeviceRemarks {
		next[k] = v
	}
	if r := strings.TrimSpace(remark); r == "" {
		delete(next, id)
	} else {
		if len([]rune(r)) > 60 {
			return errors.New("备注太长了（最多 60 个字）")
		}
		next[id] = r
	}
	cfg.DeviceRemarks = next
	return s.SaveConfig(cfg)
}

// ClearDeviceRemark 清掉某台设备的备注（设备被删除时一并清理）。
// 备注不存在也算成功——调用方只关心"删完没有"，不关心原本有没有。
func (s *Service) ClearDeviceRemark(deviceID string) error {
	id := strings.TrimSpace(deviceID)
	if id == "" {
		return nil
	}
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if _, ok := cfg.DeviceRemarks[id]; !ok {
		return nil
	}
	next := make(map[string]string, len(cfg.DeviceRemarks))
	for k, v := range cfg.DeviceRemarks {
		if k == id {
			continue
		}
		next[k] = v
	}
	cfg.DeviceRemarks = next
	return s.SaveConfig(cfg)
}
