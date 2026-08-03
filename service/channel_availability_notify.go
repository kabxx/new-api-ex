package service

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	ChannelAvailabilitySourceAutomaticDisable   = "automatic channel disable"
	ChannelAvailabilitySourceHealthCheck        = "channel health check"
	ChannelAvailabilitySourceHealthCheckPartial = "partial or cancelled channel health check"
	ChannelAvailabilitySourceManualStatus       = "manual channel status change"
	ChannelAvailabilitySourceManualBatch        = "manual channel batch change"
	ChannelAvailabilitySourceTag                = "channel tag status change"
	ChannelAvailabilitySourceCreate             = "channel created"
	ChannelAvailabilitySourceCopy               = "channel copied"
	ChannelAvailabilitySourceDelete             = "channel deleted"
	ChannelAvailabilitySourceMultiKey           = "multi-key status change"
	ChannelAvailabilitySourceOther              = "channel state change"
)

const (
	maxAvailabilityRelatedChannels       = 20
	availabilityNotificationPollInterval = 30 * time.Second
)

type ChannelAvailabilityRelatedChannel struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ChannelAvailabilityEmailResult struct {
	Recipient string `json:"recipient"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

type ChannelAvailabilityEvaluation struct {
	Transition *model.ChannelAvailabilityTransition
	Deliveries []ChannelAvailabilityEmailResult
}

var sendChannelAvailabilityEmail = common.SendEmail

var (
	availabilityNotificationWorkerOnce sync.Once
	availabilityNotificationWake       = make(chan struct{}, 1)
)

func SyncChannelAvailabilityBaseline() error {
	return model.SyncChannelAvailabilityState()
}

func EvaluateChannelAvailability(source string, related []ChannelAvailabilityRelatedChannel) (ChannelAvailabilityEvaluation, error) {
	setting := operation_setting.GetMonitorSettingSnapshot()
	recipients := []string{}
	if setting.ChannelAvailabilityNotifyEnabled {
		var err error
		recipients, err = operation_setting.NormalizeChannelAvailabilityNotifyRecipients(setting.ChannelAvailabilityNotifyRecipients)
		if err != nil {
			return ChannelAvailabilityEvaluation{}, err
		}
	}
	recipientsJSON, err := common.Marshal(recipients)
	if err != nil {
		return ChannelAvailabilityEvaluation{}, err
	}
	if len(related) > maxAvailabilityRelatedChannels {
		related = append([]ChannelAvailabilityRelatedChannel(nil), related[:maxAvailabilityRelatedChannels]...)
	}
	relatedJSON, err := common.Marshal(related)
	if err != nil {
		return ChannelAvailabilityEvaluation{}, err
	}
	event, err := model.ReconcileChannelAvailabilityNotification(model.ChannelAvailabilityNotificationInput{
		Notify:              setting.ChannelAvailabilityNotifyEnabled,
		Source:              source,
		RelatedChannelsJSON: string(relatedJSON),
		RecipientsJSON:      string(recipientsJSON),
	})
	if err != nil {
		return ChannelAvailabilityEvaluation{}, err
	}
	result := ChannelAvailabilityEvaluation{}
	if event != nil {
		result.Transition = transitionFromAvailabilityEvent(event)
		wakeChannelAvailabilityNotificationWorker()
	}
	deliveries, err := DrainChannelAvailabilityNotificationEvents()
	if err != nil {
		return result, err
	}
	if event != nil {
		result.Deliveries = deliveries[event.ID]
	}
	return result, nil
}

func transitionFromAvailabilityEvent(event *model.ChannelAvailabilityNotificationEvent) *model.ChannelAvailabilityTransition {
	return &model.ChannelAvailabilityTransition{
		FromAvailable: event.FromAvailable,
		ToAvailable:   event.ToAvailable,
		Snapshot: model.ChannelAvailabilitySnapshot{
			Available:    event.ToAvailable,
			EnabledCount: event.EnabledCount,
			TotalCount:   event.TotalCount,
		},
	}
}

func DrainChannelAvailabilityNotificationEvents() (map[int64][]ChannelAvailabilityEmailResult, error) {
	owner := common.GetRandomString(32)
	allResults := make(map[int64][]ChannelAvailabilityEmailResult)
	for {
		if !operation_setting.GetMonitorSettingSnapshot().ChannelAvailabilityNotifyEnabled {
			return allResults, nil
		}
		event, err := model.ClaimNextChannelAvailabilityNotificationEvent(owner, time.Now().Unix())
		if err != nil {
			return allResults, err
		}
		if event == nil {
			return allResults, nil
		}
		var recipients []string
		var related []ChannelAvailabilityRelatedChannel
		if err := common.UnmarshalJsonStr(event.RecipientsJSON, &recipients); err != nil {
			if err := completeMalformedAvailabilityEvent(event, owner); err != nil {
				return allResults, err
			}
			continue
		}
		if event.RelatedChannelsJSON != "" {
			if err := common.UnmarshalJsonStr(event.RelatedChannelsJSON, &related); err != nil {
				if err := completeMalformedAvailabilityEvent(event, owner); err != nil {
					return allResults, err
				}
				continue
			}
		}
		transition := transitionFromAvailabilityEvent(event)
		subject, content := buildChannelAvailabilityEmail(transition, event.Source, related, false, time.Unix(event.CreatedAt, 0))
		results := deliverChannelAvailabilityEmails(recipients, subject, content)
		resultJSON, err := common.Marshal(results)
		if err != nil {
			return allResults, err
		}
		if err := model.CompleteChannelAvailabilityNotificationEvent(event.ID, owner, string(resultJSON)); err != nil {
			return allResults, err
		}
		allResults[event.ID] = results
	}
}

func completeMalformedAvailabilityEvent(event *model.ChannelAvailabilityNotificationEvent, owner string) error {
	common.SysLog(fmt.Sprintf("channel availability notification event %d has invalid persisted payload", event.ID))
	return model.CompleteChannelAvailabilityNotificationEvent(event.ID, owner, `[{"success":false,"error":"invalid persisted payload"}]`)
}

func ResumeChannelAvailabilityNotificationEvents() {
	availabilityNotificationWorkerOnce.Do(func() {
		go channelAvailabilityNotificationWorker()
	})
	wakeChannelAvailabilityNotificationWorker()
}

func wakeChannelAvailabilityNotificationWorker() {
	select {
	case availabilityNotificationWake <- struct{}{}:
	default:
	}
}

func channelAvailabilityNotificationWorker() {
	ticker := time.NewTicker(availabilityNotificationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-availabilityNotificationWake:
		case <-ticker.C:
		}
		resumeChannelAvailabilityNotificationEvents()
	}
}

func resumeChannelAvailabilityNotificationEvents() {
	for {
		if !operation_setting.GetMonitorSettingSnapshot().ChannelAvailabilityNotifyEnabled {
			return
		}
		if _, err := DrainChannelAvailabilityNotificationEvents(); err != nil {
			common.SysLog("failed to resume channel availability notifications: " + err.Error())
			return
		}
		if !operation_setting.GetMonitorSettingSnapshot().ChannelAvailabilityNotifyEnabled {
			return
		}
		now := time.Now()
		retryAt, err := model.GetChannelAvailabilityNotificationRetryAt(now.Unix())
		if err != nil {
			common.SysLog("failed to schedule channel availability notification recovery: " + err.Error())
			return
		}
		if retryAt == 0 {
			return
		}
		delay := time.Until(time.Unix(retryAt, 0))
		if delay <= 0 {
			delay = 100 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		<-timer.C
	}
}

func SendChannelAvailabilityTestEmails(values []string) ([]ChannelAvailabilityEmailResult, error) {
	recipients, err := operation_setting.NormalizeChannelAvailabilityNotifyRecipients(values)
	if err != nil {
		return nil, err
	}
	snapshot, err := model.GetChannelAvailabilitySnapshot()
	if err != nil {
		return nil, err
	}
	transition := &model.ChannelAvailabilityTransition{
		FromAvailable: !snapshot.Available,
		ToAvailable:   snapshot.Available,
		Snapshot:      snapshot,
	}
	subject, content := buildChannelAvailabilityEmail(transition, "test email", nil, true, time.Now())
	return deliverChannelAvailabilityEmails(recipients, subject, content), nil
}

func deliverChannelAvailabilityEmails(recipients []string, subject, content string) []ChannelAvailabilityEmailResult {
	results := make([]ChannelAvailabilityEmailResult, 0, len(recipients))
	for _, recipient := range recipients {
		delivery := ChannelAvailabilityEmailResult{Recipient: recipient}
		if err := sendChannelAvailabilityEmail(subject, recipient, content); err != nil {
			delivery.Error = "email delivery failed"
			common.SysLog(fmt.Sprintf("channel availability email delivery failed for %s", maskNotificationEmail(recipient)))
		} else {
			delivery.Success = true
			common.SysLog(fmt.Sprintf("channel availability email delivered to %s", maskNotificationEmail(recipient)))
		}
		results = append(results, delivery)
	}
	return results
}

func buildChannelAvailabilityEmail(transition *model.ChannelAvailabilityTransition, source string, related []ChannelAvailabilityRelatedChannel, test bool, occurredAt time.Time) (string, string) {
	stateLabel := "所有路由不可用"
	stateDescription := "系统当前没有处于启用状态的渠道，请尽快检查渠道配置和上游服务。"
	if transition.ToAvailable {
		stateLabel = "路由已恢复"
		stateDescription = "系统已恢复至少一个处于启用状态的渠道。"
	}
	if test {
		stateLabel = "可用性通知测试"
		stateDescription = "这是一封测试邮件，用于验证渠道可用性通知的收件人与 SMTP 配置。"
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	subject := fmt.Sprintf("[%s] %s", common.SystemName, stateLabel)
	if test {
		subject = fmt.Sprintf("[%s] [测试] 渠道可用性通知", common.SystemName)
	}

	var channelList strings.Builder
	if len(related) > 0 {
		channelList.WriteString("<h3>相关渠道</h3><ul>")
		limit := len(related)
		if limit > maxAvailabilityRelatedChannels {
			limit = maxAvailabilityRelatedChannels
		}
		for _, channel := range related[:limit] {
			channelList.WriteString(fmt.Sprintf("<li>#%d %s</li>", channel.ID, html.EscapeString(channel.Name)))
		}
		if len(related) > limit {
			channelList.WriteString(fmt.Sprintf("<li>另有 %d 个渠道</li>", len(related)-limit))
		}
		channelList.WriteString("</ul>")
	}

	content := fmt.Sprintf(
		"<h2>%s</h2><p>%s</p><table><tr><td>系统</td><td>%s</td></tr><tr><td>发生时间</td><td>%s</td></tr><tr><td>当前启用渠道</td><td>%d</td></tr><tr><td>渠道总数</td><td>%d</td></tr><tr><td>触发来源</td><td>%s</td></tr></table>%s",
		html.EscapeString(stateLabel),
		html.EscapeString(stateDescription),
		html.EscapeString(common.SystemName),
		html.EscapeString(occurredAt.Format(time.RFC1123Z)),
		transition.Snapshot.EnabledCount,
		transition.Snapshot.TotalCount,
		html.EscapeString(channelAvailabilitySourceLabel(source)),
		channelList.String(),
	)
	return subject, content
}

func channelAvailabilitySourceLabel(source string) string {
	switch source {
	case ChannelAvailabilitySourceAutomaticDisable:
		return "真实请求自动禁用"
	case ChannelAvailabilitySourceHealthCheck:
		return "渠道健康检查"
	case ChannelAvailabilitySourceHealthCheckPartial:
		return "已取消或部分完成的渠道健康检查"
	case ChannelAvailabilitySourceManualStatus:
		return "人工渠道状态变更"
	case ChannelAvailabilitySourceManualBatch:
		return "人工批量渠道状态变更"
	case ChannelAvailabilitySourceTag:
		return "渠道标签状态变更"
	case ChannelAvailabilitySourceCreate:
		return "新增渠道"
	case ChannelAvailabilitySourceCopy:
		return "复制渠道"
	case ChannelAvailabilitySourceDelete:
		return "删除渠道"
	case ChannelAvailabilitySourceMultiKey:
		return "多 Key 状态变更"
	case "test email":
		return "测试邮件"
	default:
		return "渠道状态变更"
	}
}

func maskNotificationEmail(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return "***"
	}
	local := address[:at]
	if len(local) > 1 {
		local = local[:1] + "***"
	} else {
		local = "***"
	}
	return local + address[at:]
}
