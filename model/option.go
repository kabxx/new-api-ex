package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/performance_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
)

type Option struct {
	Key   string `json:"key" gorm:"primaryKey"`
	Value string `json:"value"`
}

var routingReliabilityBulkOptionKeys = map[string]struct{}{
	"RetryTimes": {}, "ChannelDisableThreshold": {},
	"AutomaticDisableChannelEnabled": {}, "AutoDisableTolerance": {},
	"AutomaticEnableChannelEnabled": {}, "AutomaticDisableKeywords": {},
	"AutomaticDisableStatusCodes": {}, "AutomaticRetryStatusCodes": {},
	"retry_setting.time_budget_seconds": {},
	"retry_setting.delay_strategy":      {}, "retry_setting.fixed_delay_milliseconds": {},
	"retry_setting.exponential_base_delay_milliseconds": {},
	"retry_setting.exponential_max_delay_milliseconds":  {},
	"retry_setting.jitter_percent":                      {}, "retry_setting.respect_retry_after": {},
	"retry_setting.channel_strategy": {}, "retry_setting.exhausted_action": {},
	"retry_setting.try_other_keys":              {},
	"monitor_setting.auto_test_channel_enabled": {},
	"monitor_setting.auto_test_channel_minutes": {},
	"monitor_setting.channel_test_mode":         {}, "monitor_setting.zero_token_as_failure": {},
	"monitor_setting.channel_availability_notify_enabled":    {},
	"monitor_setting.channel_availability_notify_recipients": {},
}

func IsRoutingReliabilityBulkOptionKey(key string) bool {
	_, ok := routingReliabilityBulkOptionKeys[key]
	return ok
}

type parsedRoutingReliabilityOptions struct {
	retryTimes                    int
	hasRetryTimes                 bool
	channelDisableThreshold       float64
	hasChannelDisableThreshold    bool
	autoDisableChannelEnabled     bool
	hasAutoDisableChannelEnabled  bool
	autoEnableChannelEnabled      bool
	hasAutoEnableChannelEnabled   bool
	autoDisableTolerance          int
	hasAutoDisableTolerance       bool
	automaticDisableKeywords      string
	hasAutomaticDisableKeywords   bool
	automaticDisableStatusCodes   []operation_setting.StatusCodeRange
	hasAutomaticDisableStatusCode bool
	automaticRetryStatusCodes     []operation_setting.StatusCodeRange
	hasAutomaticRetryStatusCode   bool
	retryValues                   map[string]string
	monitorValues                 map[string]string
}

func AllOption() ([]*Option, error) {
	var options []*Option
	var err error
	err = DB.Find(&options).Error
	return options, err
}

func InitOptionMap() {
	common.OptionMapRWMutex.Lock()
	common.OptionMap = make(map[string]string)

	// 添加原有的系统配置
	common.OptionMap["FileUploadPermission"] = strconv.Itoa(common.FileUploadPermission)
	common.OptionMap["FileDownloadPermission"] = strconv.Itoa(common.FileDownloadPermission)
	common.OptionMap["ImageUploadPermission"] = strconv.Itoa(common.ImageUploadPermission)
	common.OptionMap["ImageDownloadPermission"] = strconv.Itoa(common.ImageDownloadPermission)
	common.OptionMap["PasswordLoginEnabled"] = strconv.FormatBool(common.PasswordLoginEnabled)
	common.OptionMap["PasswordRegisterEnabled"] = strconv.FormatBool(common.PasswordRegisterEnabled)
	common.OptionMap["EmailVerificationEnabled"] = strconv.FormatBool(common.EmailVerificationEnabled)
	common.OptionMap["GitHubOAuthEnabled"] = strconv.FormatBool(common.GitHubOAuthEnabled)
	common.OptionMap["LinuxDOOAuthEnabled"] = strconv.FormatBool(common.LinuxDOOAuthEnabled)
	common.OptionMap["TelegramOAuthEnabled"] = strconv.FormatBool(common.TelegramOAuthEnabled)
	common.OptionMap["WeChatAuthEnabled"] = strconv.FormatBool(common.WeChatAuthEnabled)
	common.OptionMap["TurnstileCheckEnabled"] = strconv.FormatBool(common.TurnstileCheckEnabled)
	common.OptionMap["RegisterEnabled"] = strconv.FormatBool(common.RegisterEnabled)
	common.OptionMap["AutomaticDisableChannelEnabled"] = strconv.FormatBool(common.AutomaticDisableChannelEnabled)
	common.OptionMap["AutomaticEnableChannelEnabled"] = strconv.FormatBool(common.AutomaticEnableChannelEnabled)
	common.OptionMap["LogConsumeEnabled"] = strconv.FormatBool(common.LogConsumeEnabled)
	common.OptionMap["DisplayInCurrencyEnabled"] = strconv.FormatBool(common.DisplayInCurrencyEnabled)
	common.OptionMap["DisplayTokenStatEnabled"] = strconv.FormatBool(common.DisplayTokenStatEnabled)
	common.OptionMap["DrawingEnabled"] = strconv.FormatBool(common.DrawingEnabled)
	common.OptionMap["TaskEnabled"] = strconv.FormatBool(common.TaskEnabled)
	common.OptionMap["DataExportEnabled"] = strconv.FormatBool(common.DataExportEnabled)
	common.OptionMap["ChannelDisableThreshold"] = strconv.FormatFloat(common.ChannelDisableThreshold, 'f', -1, 64)
	common.OptionMap["EmailDomainRestrictionEnabled"] = strconv.FormatBool(common.EmailDomainRestrictionEnabled)
	common.OptionMap["EmailAliasRestrictionEnabled"] = strconv.FormatBool(common.EmailAliasRestrictionEnabled)
	common.OptionMap["EmailDomainWhitelist"] = strings.Join(common.EmailDomainWhitelist, ",")
	common.OptionMap["SMTPServer"] = ""
	common.OptionMap["SMTPFrom"] = ""
	common.OptionMap["SMTPPort"] = strconv.Itoa(common.SMTPPort)
	common.OptionMap["SMTPAccount"] = ""
	common.OptionMap["SMTPToken"] = ""
	common.OptionMap["SMTPSSLEnabled"] = strconv.FormatBool(common.SMTPSSLEnabled)
	common.OptionMap["SMTPStartTLSEnabled"] = strconv.FormatBool(common.SMTPStartTLSEnabled)
	common.OptionMap["SMTPInsecureSkipVerify"] = strconv.FormatBool(common.SMTPInsecureSkipVerify)
	common.OptionMap["SMTPForceAuthLogin"] = strconv.FormatBool(common.SMTPForceAuthLogin)
	common.OptionMap["Notice"] = ""
	common.OptionMap["About"] = ""
	common.OptionMap["HomePageContent"] = ""
	common.OptionMap["Footer"] = common.Footer
	common.OptionMap["SystemName"] = common.SystemName
	common.OptionMap["Logo"] = common.Logo
	common.OptionMap["ServerAddress"] = ""
	common.OptionMap["WorkerUrl"] = system_setting.WorkerUrl
	common.OptionMap["WorkerValidKey"] = system_setting.WorkerValidKey
	common.OptionMap["WorkerAllowHttpImageRequestEnabled"] = strconv.FormatBool(system_setting.WorkerAllowHttpImageRequestEnabled)
	common.OptionMap["PayAddress"] = ""
	common.OptionMap["CustomCallbackAddress"] = ""
	common.OptionMap["EpayId"] = ""
	common.OptionMap["EpayKey"] = ""
	common.OptionMap["Price"] = strconv.FormatFloat(operation_setting.Price, 'f', -1, 64)
	common.OptionMap["USDExchangeRate"] = strconv.FormatFloat(operation_setting.USDExchangeRate, 'f', -1, 64)
	common.OptionMap["MinTopUp"] = strconv.Itoa(operation_setting.MinTopUp)
	common.OptionMap["StripeMinTopUp"] = strconv.Itoa(setting.StripeMinTopUp)
	common.OptionMap["StripeApiSecret"] = setting.StripeApiSecret
	common.OptionMap["StripeWebhookSecret"] = setting.StripeWebhookSecret
	common.OptionMap["StripePriceId"] = setting.StripePriceId
	common.OptionMap["StripeUnitPrice"] = strconv.FormatFloat(setting.StripeUnitPrice, 'f', -1, 64)
	common.OptionMap["StripePromotionCodesEnabled"] = strconv.FormatBool(setting.StripePromotionCodesEnabled)
	common.OptionMap["CreemApiKey"] = setting.CreemApiKey
	common.OptionMap["CreemProducts"] = setting.CreemProducts
	common.OptionMap["CreemTestMode"] = strconv.FormatBool(setting.CreemTestMode)
	common.OptionMap["CreemWebhookSecret"] = setting.CreemWebhookSecret
	common.OptionMap["WaffoEnabled"] = strconv.FormatBool(setting.WaffoEnabled)
	common.OptionMap["WaffoApiKey"] = setting.WaffoApiKey
	common.OptionMap["WaffoPrivateKey"] = setting.WaffoPrivateKey
	common.OptionMap["WaffoPublicCert"] = setting.WaffoPublicCert
	common.OptionMap["WaffoSandboxPublicCert"] = setting.WaffoSandboxPublicCert
	common.OptionMap["WaffoSandboxApiKey"] = setting.WaffoSandboxApiKey
	common.OptionMap["WaffoSandboxPrivateKey"] = setting.WaffoSandboxPrivateKey
	common.OptionMap["WaffoSandbox"] = strconv.FormatBool(setting.WaffoSandbox)
	common.OptionMap["WaffoMerchantId"] = setting.WaffoMerchantId
	common.OptionMap["WaffoNotifyUrl"] = setting.WaffoNotifyUrl
	common.OptionMap["WaffoReturnUrl"] = setting.WaffoReturnUrl
	common.OptionMap["WaffoSubscriptionReturnUrl"] = setting.WaffoSubscriptionReturnUrl
	common.OptionMap["WaffoCurrency"] = setting.WaffoCurrency
	common.OptionMap["WaffoUnitPrice"] = strconv.FormatFloat(setting.WaffoUnitPrice, 'f', -1, 64)
	common.OptionMap["WaffoMinTopUp"] = strconv.Itoa(setting.WaffoMinTopUp)
	common.OptionMap["WaffoPayMethods"] = setting.WaffoPayMethods2JsonString()
	common.OptionMap["WaffoPancakeMerchantID"] = setting.WaffoPancakeMerchantID
	common.OptionMap["WaffoPancakePrivateKey"] = setting.WaffoPancakePrivateKey
	common.OptionMap["WaffoPancakeReturnURL"] = setting.WaffoPancakeReturnURL
	common.OptionMap["WaffoPancakeUnitPrice"] = strconv.FormatFloat(setting.WaffoPancakeUnitPrice, 'f', -1, 64)
	common.OptionMap["WaffoPancakeMinTopUp"] = strconv.Itoa(setting.WaffoPancakeMinTopUp)
	common.OptionMap["WaffoPancakeStoreID"] = setting.WaffoPancakeStoreID
	common.OptionMap["WaffoPancakeProductID"] = setting.WaffoPancakeProductID
	common.OptionMap["TopupGroupRatio"] = common.TopupGroupRatio2JSONString()
	common.OptionMap["Chats"] = setting.Chats2JsonString()
	common.OptionMap["AutoGroups"] = setting.AutoGroups2JsonString()
	common.OptionMap["DefaultUseAutoGroup"] = strconv.FormatBool(setting.DefaultUseAutoGroup)
	common.OptionMap["PayMethods"] = operation_setting.PayMethods2JsonString()
	common.OptionMap["GitHubClientId"] = ""
	common.OptionMap["GitHubClientSecret"] = ""
	common.OptionMap["TelegramBotToken"] = ""
	common.OptionMap["TelegramBotName"] = ""
	common.OptionMap["WeChatServerAddress"] = ""
	common.OptionMap["WeChatServerToken"] = ""
	common.OptionMap["WeChatAccountQRCodeImageURL"] = ""
	common.OptionMap["TurnstileSiteKey"] = ""
	common.OptionMap["TurnstileSecretKey"] = ""
	common.OptionMap["QuotaForNewUser"] = strconv.Itoa(common.QuotaForNewUser)
	common.OptionMap["QuotaForInviter"] = strconv.Itoa(common.QuotaForInviter)
	common.OptionMap["QuotaForInvitee"] = strconv.Itoa(common.QuotaForInvitee)
	common.OptionMap["QuotaRemindThreshold"] = strconv.Itoa(common.QuotaRemindThreshold)
	common.OptionMap["PreConsumedQuota"] = strconv.Itoa(common.PreConsumedQuota)
	common.OptionMap["ModelRequestRateLimitCount"] = strconv.Itoa(setting.ModelRequestRateLimitCount)
	common.OptionMap["ModelRequestRateLimitDurationMinutes"] = strconv.Itoa(setting.ModelRequestRateLimitDurationMinutes)
	common.OptionMap["ModelRequestRateLimitSuccessCount"] = strconv.Itoa(setting.ModelRequestRateLimitSuccessCount)
	common.OptionMap["ModelRequestRateLimitGroup"] = setting.ModelRequestRateLimitGroup2JSONString()
	common.OptionMap["ModelRatio"] = ratio_setting.ModelRatio2JSONString()
	common.OptionMap["ModelPrice"] = ratio_setting.ModelPrice2JSONString()
	common.OptionMap["CacheRatio"] = ratio_setting.CacheRatio2JSONString()
	common.OptionMap["CreateCacheRatio"] = ratio_setting.CreateCacheRatio2JSONString()
	common.OptionMap["GroupRatio"] = ratio_setting.GroupRatio2JSONString()
	common.OptionMap["GroupGroupRatio"] = ratio_setting.GroupGroupRatio2JSONString()
	common.OptionMap["UserUsableGroups"] = setting.UserUsableGroups2JSONString()
	common.OptionMap["CompletionRatio"] = ratio_setting.CompletionRatio2JSONString()
	common.OptionMap["ImageRatio"] = ratio_setting.ImageRatio2JSONString()
	common.OptionMap["AudioRatio"] = ratio_setting.AudioRatio2JSONString()
	common.OptionMap["AudioCompletionRatio"] = ratio_setting.AudioCompletionRatio2JSONString()
	common.OptionMap["TopUpLink"] = common.TopUpLink
	//common.OptionMap["ChatLink"] = common.ChatLink
	//common.OptionMap["ChatLink2"] = common.ChatLink2
	common.OptionMap["QuotaPerUnit"] = strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
	common.OptionMap["RetryTimes"] = strconv.Itoa(common.RetryTimes)
	common.OptionMap["AutoDisableTolerance"] = strconv.Itoa(common.AutoDisableTolerance)
	common.OptionMap["DataExportInterval"] = strconv.Itoa(common.DataExportInterval)
	common.OptionMap["DataExportDefaultTime"] = common.DataExportDefaultTime
	common.OptionMap["DefaultCollapseSidebar"] = strconv.FormatBool(common.DefaultCollapseSidebar)
	common.OptionMap["MjNotifyEnabled"] = strconv.FormatBool(setting.MjNotifyEnabled)
	common.OptionMap["MjAccountFilterEnabled"] = strconv.FormatBool(setting.MjAccountFilterEnabled)
	common.OptionMap["MjModeClearEnabled"] = strconv.FormatBool(setting.MjModeClearEnabled)
	common.OptionMap["MjForwardUrlEnabled"] = strconv.FormatBool(setting.MjForwardUrlEnabled)
	common.OptionMap["MjActionCheckSuccessEnabled"] = strconv.FormatBool(setting.MjActionCheckSuccessEnabled)
	common.OptionMap["CheckSensitiveEnabled"] = strconv.FormatBool(setting.CheckSensitiveEnabled)
	common.OptionMap["DemoSiteEnabled"] = strconv.FormatBool(operation_setting.DemoSiteEnabled)
	common.OptionMap["SelfUseModeEnabled"] = strconv.FormatBool(operation_setting.SelfUseModeEnabled)
	common.OptionMap["ModelRequestRateLimitEnabled"] = strconv.FormatBool(setting.ModelRequestRateLimitEnabled)
	common.OptionMap["CheckSensitiveOnPromptEnabled"] = strconv.FormatBool(setting.CheckSensitiveOnPromptEnabled)
	common.OptionMap["StopOnSensitiveEnabled"] = strconv.FormatBool(setting.StopOnSensitiveEnabled)
	common.OptionMap["SensitiveWords"] = setting.SensitiveWordsToString()
	common.OptionMap["StreamCacheQueueLength"] = strconv.Itoa(setting.StreamCacheQueueLength)
	common.OptionMap["AutomaticDisableKeywords"] = operation_setting.AutomaticDisableKeywordsToString()
	common.OptionMap["AutomaticDisableStatusCodes"] = operation_setting.AutomaticDisableStatusCodesToString()
	common.OptionMap["AutomaticRetryStatusCodes"] = operation_setting.AutomaticRetryStatusCodesToString()
	common.OptionMap["ExposeRatioEnabled"] = strconv.FormatBool(ratio_setting.IsExposeRatioEnabled())

	// 自动添加所有注册的模型配置
	modelConfigs := config.GlobalConfig.ExportAllConfigs()
	for k, v := range modelConfigs {
		common.OptionMap[k] = v
	}

	common.OptionMapRWMutex.Unlock()
	loadOptionsFromDatabase()
}

func loadOptionsFromDatabase() {
	options, _ := AllOption()
	for _, option := range options {
		err := updateOptionMap(option.Key, option.Value)
		if err != nil {
			common.SysLog("failed to update option map: " + err.Error())
		}
	}
}

func SyncOptions(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing options from database")
		loadOptionsFromDatabase()
	}
}

func parseAutoDisableTolerance(value string) (int, error) {
	tolerance, err := strconv.Atoi(value)
	if err != nil || tolerance < 0 || tolerance > common.MaxAutoDisableTolerance {
		return 0, fmt.Errorf("AutoDisableTolerance must be an integer between 0 and %d", common.MaxAutoDisableTolerance)
	}
	return tolerance, nil
}

func parseRetryTimes(value string) (int, error) {
	parsed, err := strconv.ParseInt(value, 10, strconv.IntSize)
	if err != nil || parsed < -1 {
		return 0, fmt.Errorf("RetryTimes must be -1 or a non-negative integer")
	}
	return int(parsed), nil
}

func validateOptionValue(key string, value string) error {
	switch key {
	case operation_setting.ToolPriceOptionKey:
		return operation_setting.ValidateToolPricesJSON(value)
	case "AutoDisableTolerance":
		_, err := parseAutoDisableTolerance(value)
		return err
	case "ChannelDisableThreshold":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			return fmt.Errorf("ChannelDisableThreshold must be a non-negative finite number")
		}
		return nil
	case "AutomaticDisableChannelEnabled", "AutomaticEnableChannelEnabled":
		_, err := strconv.ParseBool(value)
		return err
	case "AutomaticDisableStatusCodes", "AutomaticRetryStatusCodes":
		_, err := operation_setting.ParseHTTPStatusCodeRanges(value)
		return err
	case "AutomaticDisableKeywords":
		return nil
	default:
		if strings.HasPrefix(key, "monitor_setting.") {
			return operation_setting.ValidateMonitorOption(key, value)
		}
		if strings.HasPrefix(key, "retry_setting.") {
			return operation_setting.ValidateRetryOption(key, value)
		}
		if key == "RetryTimes" {
			_, err := parseRetryTimes(value)
			return err
		}
		return nil
	}
}

func validateChannelAvailabilityNotificationCombination(values map[string]string) error {
	const (
		enabledKey    = "monitor_setting.channel_availability_notify_enabled"
		recipientsKey = "monitor_setting.channel_availability_notify_recipients"
	)
	_, updatesEnabled := values[enabledKey]
	_, updatesRecipients := values[recipientsKey]
	if !updatesEnabled && !updatesRecipients {
		return nil
	}

	setting := operation_setting.GetMonitorSettingSnapshot()
	enabled := setting.ChannelAvailabilityNotifyEnabled
	recipients := setting.ChannelAvailabilityNotifyRecipients
	if value, ok := values[enabledKey]; ok {
		enabled, _ = strconv.ParseBool(value)
	}
	if value, ok := values[recipientsKey]; ok {
		if err := common.UnmarshalJsonStr(value, &recipients); err != nil {
			return err
		}
	}
	if !enabled {
		return nil
	}
	if _, err := operation_setting.NormalizeChannelAvailabilityNotifyRecipients(recipients); err != nil {
		return fmt.Errorf("channel availability notification recipients are required when notifications are enabled: %w", err)
	}
	return nil
}

func prepareRoutingReliabilityOptions(values map[string]string) (map[string]string, parsedRoutingReliabilityOptions, error) {
	prepared := make(map[string]string, len(values))
	parsed := parsedRoutingReliabilityOptions{
		retryValues:   make(map[string]string),
		monitorValues: make(map[string]string),
	}
	for key, value := range values {
		if !IsRoutingReliabilityBulkOptionKey(key) {
			return nil, parsed, fmt.Errorf("option %s is not allowed in the routing reliability bulk update", key)
		}
		if err := validateOptionValue(key, value); err != nil {
			return nil, parsed, err
		}
		prepared[key] = value
		switch key {
		case "RetryTimes":
			parsed.retryTimes, _ = parseRetryTimes(value)
			parsed.hasRetryTimes = true
		case "ChannelDisableThreshold":
			parsed.channelDisableThreshold, _ = strconv.ParseFloat(value, 64)
			parsed.hasChannelDisableThreshold = true
		case "AutomaticDisableChannelEnabled":
			parsed.autoDisableChannelEnabled, _ = strconv.ParseBool(value)
			parsed.hasAutoDisableChannelEnabled = true
		case "AutomaticEnableChannelEnabled":
			parsed.autoEnableChannelEnabled, _ = strconv.ParseBool(value)
			parsed.hasAutoEnableChannelEnabled = true
		case "AutoDisableTolerance":
			parsed.autoDisableTolerance, _ = parseAutoDisableTolerance(value)
			parsed.hasAutoDisableTolerance = true
		case "AutomaticDisableKeywords":
			parsed.automaticDisableKeywords = value
			parsed.hasAutomaticDisableKeywords = true
		case "AutomaticDisableStatusCodes":
			parsed.automaticDisableStatusCodes, _ = operation_setting.ParseHTTPStatusCodeRanges(value)
			parsed.hasAutomaticDisableStatusCode = true
		case "AutomaticRetryStatusCodes":
			parsed.automaticRetryStatusCodes, _ = operation_setting.ParseHTTPStatusCodeRanges(value)
			parsed.hasAutomaticRetryStatusCode = true
		default:
			if strings.HasPrefix(key, "retry_setting.") {
				parsed.retryValues[strings.TrimPrefix(key, "retry_setting.")] = value
				continue
			}
			if strings.HasPrefix(key, "monitor_setting.") {
				configKey := strings.TrimPrefix(key, "monitor_setting.")
				if key == "monitor_setting.channel_availability_notify_recipients" {
					var recipients []string
					_ = common.UnmarshalJsonStr(value, &recipients)
					if len(recipients) > 0 {
						normalized, err := operation_setting.NormalizeChannelAvailabilityNotifyRecipients(recipients)
						if err != nil {
							return nil, parsed, err
						}
						encoded, err := common.Marshal(normalized)
						if err != nil {
							return nil, parsed, err
						}
						value = string(encoded)
						prepared[key] = value
					}
				}
				parsed.monitorValues[configKey] = value
			}
		}
	}
	if err := validateChannelAvailabilityNotificationCombination(prepared); err != nil {
		return nil, parsed, err
	}
	if len(parsed.monitorValues) > 0 && config.GlobalConfig.Get("monitor_setting") == nil {
		return nil, parsed, fmt.Errorf("monitor setting is not registered")
	}
	if len(parsed.retryValues) > 0 && config.GlobalConfig.Get("retry_setting") == nil {
		return nil, parsed, fmt.Errorf("retry setting is not registered")
	}
	return prepared, parsed, nil
}

func publishRoutingReliabilityOptions(values map[string]string, parsed parsedRoutingReliabilityOptions) {
	common.OptionMapRWMutex.Lock()
	for key, value := range values {
		common.OptionMap[key] = value
	}
	common.OptionMapRWMutex.Unlock()

	if parsed.hasRetryTimes {
		common.RetryTimes = parsed.retryTimes
	}
	if parsed.hasChannelDisableThreshold {
		common.ChannelDisableThreshold = parsed.channelDisableThreshold
	}
	if parsed.hasAutoDisableChannelEnabled {
		common.AutomaticDisableChannelEnabled = parsed.autoDisableChannelEnabled
	}
	if parsed.hasAutoEnableChannelEnabled {
		common.AutomaticEnableChannelEnabled = parsed.autoEnableChannelEnabled
	}
	if parsed.hasAutoDisableTolerance {
		common.AutoDisableTolerance = parsed.autoDisableTolerance
	}
	if parsed.hasAutomaticDisableKeywords {
		operation_setting.AutomaticDisableKeywordsFromString(parsed.automaticDisableKeywords)
	}
	if parsed.hasAutomaticDisableStatusCode {
		operation_setting.AutomaticDisableStatusCodeRanges = append([]operation_setting.StatusCodeRange(nil), parsed.automaticDisableStatusCodes...)
	}
	if parsed.hasAutomaticRetryStatusCode {
		operation_setting.AutomaticRetryStatusCodeRanges = append([]operation_setting.StatusCodeRange(nil), parsed.automaticRetryStatusCodes...)
	}
	if len(parsed.monitorValues) > 0 {
		config.GlobalConfig.Update("monitor_setting", parsed.monitorValues)
	}
	if len(parsed.retryValues) > 0 {
		config.GlobalConfig.Update("retry_setting", parsed.retryValues)
	}
}

func syncAvailabilityOnNotificationToggle(tx *gorm.DB, values map[string]string, currentEnabled bool) error {
	value, ok := values["monitor_setting.channel_availability_notify_enabled"]
	if !ok {
		return nil
	}
	enabled, _ := strconv.ParseBool(value)
	if enabled == currentEnabled {
		return nil
	}
	if err := CancelPendingChannelAvailabilityNotificationEventsWithDB(tx); err != nil {
		return err
	}
	return SyncChannelAvailabilityStateWithDB(tx)
}

func UpdateRoutingReliabilityOptionsBulk(values map[string]string) error {
	if len(values) == 0 {
		return fmt.Errorf("at least one option is required")
	}
	prepared, parsed, err := prepareRoutingReliabilityOptions(values)
	if err != nil {
		return err
	}

	currentMonitorSetting := operation_setting.GetMonitorSettingSnapshot()
	if err := DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range prepared {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			option.Value = value
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return syncAvailabilityOnNotificationToggle(tx, prepared, currentMonitorSetting.ChannelAvailabilityNotifyEnabled)
	}); err != nil {
		return err
	}

	publishRoutingReliabilityOptions(prepared, parsed)
	return nil
}

func UpdateOption(key string, value string) error {
	if err := validateOptionValue(key, value); err != nil {
		return err
	}
	if err := validateChannelAvailabilityNotificationCombination(map[string]string{key: value}); err != nil {
		return err
	}
	currentMonitorSetting := operation_setting.GetMonitorSettingSnapshot()
	err := DB.Transaction(func(tx *gorm.DB) error {
		option := Option{Key: key}
		if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
			return err
		}
		option.Value = value
		if err := tx.Save(&option).Error; err != nil {
			return err
		}
		return syncAvailabilityOnNotificationToggle(tx, map[string]string{key: value}, currentMonitorSetting.ChannelAvailabilityNotifyEnabled)
	})
	if err != nil {
		return err
	}
	// Update OptionMap
	if err := updateOptionMap(key, value); err != nil {
		return err
	}
	return nil
}

// UpdateOptionsBulk persists multiple key/value pairs in a single database
// transaction, then dispatches them through updateOptionMap in one pass. If
// any DB write fails the whole transaction rolls back and no in-memory state
// is touched — safe for callers that must commit a set of related options
// atomically (e.g. payment gateway binding).
func UpdateOptionsBulk(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	allRoutingReliabilityOptions := true
	for key := range values {
		if !IsRoutingReliabilityBulkOptionKey(key) {
			allRoutingReliabilityOptions = false
			break
		}
	}
	if allRoutingReliabilityOptions {
		return UpdateRoutingReliabilityOptionsBulk(values)
	}
	for key, value := range values {
		if err := validateOptionValue(key, value); err != nil {
			return err
		}
	}
	if err := validateChannelAvailabilityNotificationCombination(values); err != nil {
		return err
	}
	currentMonitorSetting := operation_setting.GetMonitorSettingSnapshot()
	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range values {
			option := Option{Key: k}
			if err := tx.FirstOrCreate(&option, Option{Key: k}).Error; err != nil {
				return err
			}
			option.Value = v
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return syncAvailabilityOnNotificationToggle(tx, values, currentMonitorSetting.ChannelAvailabilityNotifyEnabled)
	})
	if err != nil {
		return err
	}
	retryValues := make(map[string]string)
	monitorValues := make(map[string]string)
	for k, v := range values {
		if strings.HasPrefix(k, "retry_setting.") {
			retryValues[strings.TrimPrefix(k, "retry_setting.")] = v
			continue
		}
		if strings.HasPrefix(k, "monitor_setting.") {
			monitorValues[strings.TrimPrefix(k, "monitor_setting.")] = v
			continue
		}
		if err := updateOptionMap(k, v); err != nil {
			return err
		}
	}
	if len(monitorValues) > 0 {
		common.OptionMapRWMutex.Lock()
		for key, value := range values {
			if strings.HasPrefix(key, "monitor_setting.") {
				common.OptionMap[key] = value
			}
		}
		common.OptionMapRWMutex.Unlock()
		if !config.GlobalConfig.Update("monitor_setting", monitorValues) {
			return fmt.Errorf("monitor setting is not registered")
		}
	}
	if len(retryValues) > 0 {
		common.OptionMapRWMutex.Lock()
		for key, value := range values {
			if strings.HasPrefix(key, "retry_setting.") {
				common.OptionMap[key] = value
			}
		}
		common.OptionMapRWMutex.Unlock()
		if !config.GlobalConfig.Update("retry_setting", retryValues) {
			return fmt.Errorf("retry setting is not registered")
		}
	}
	return nil
}

func updateOptionMap(key string, value string) (err error) {
	var autoDisableTolerance int
	var retryTimes int
	if key == "AutoDisableTolerance" {
		autoDisableTolerance, err = parseAutoDisableTolerance(value)
		if err != nil {
			return err
		}
	}
	if key == "RetryTimes" {
		retryTimes, err = parseRetryTimes(value)
		if err != nil {
			return err
		}
	}
	if key == retiredThemeOptionKey {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, key)
		common.OptionMapRWMutex.Unlock()
		return nil
	}
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	common.OptionMap[key] = value

	// 检查是否是模型配置 - 使用更规范的方式处理
	if handleConfigUpdate(key, value) {
		return nil // 已由配置系统处理
	}

	// 处理传统配置项...
	if strings.HasSuffix(key, "Permission") {
		intValue, _ := strconv.Atoi(value)
		switch key {
		case "FileUploadPermission":
			common.FileUploadPermission = intValue
		case "FileDownloadPermission":
			common.FileDownloadPermission = intValue
		case "ImageUploadPermission":
			common.ImageUploadPermission = intValue
		case "ImageDownloadPermission":
			common.ImageDownloadPermission = intValue
		}
	}
	if strings.HasSuffix(key, "Enabled") || key == "DefaultCollapseSidebar" || key == "DefaultUseAutoGroup" || key == "SMTPForceAuthLogin" || key == "SMTPInsecureSkipVerify" {
		boolValue := value == "true"
		switch key {
		case "PasswordRegisterEnabled":
			common.PasswordRegisterEnabled = boolValue
		case "PasswordLoginEnabled":
			common.PasswordLoginEnabled = boolValue
		case "EmailVerificationEnabled":
			common.EmailVerificationEnabled = boolValue
		case "GitHubOAuthEnabled":
			common.GitHubOAuthEnabled = boolValue
		case "LinuxDOOAuthEnabled":
			common.LinuxDOOAuthEnabled = boolValue
		case "WeChatAuthEnabled":
			common.WeChatAuthEnabled = boolValue
		case "TelegramOAuthEnabled":
			common.TelegramOAuthEnabled = boolValue
		case "TurnstileCheckEnabled":
			common.TurnstileCheckEnabled = boolValue
		case "RegisterEnabled":
			common.RegisterEnabled = boolValue
		case "EmailDomainRestrictionEnabled":
			common.EmailDomainRestrictionEnabled = boolValue
		case "EmailAliasRestrictionEnabled":
			common.EmailAliasRestrictionEnabled = boolValue
		case "AutomaticDisableChannelEnabled":
			common.AutomaticDisableChannelEnabled = boolValue
		case "AutomaticEnableChannelEnabled":
			common.AutomaticEnableChannelEnabled = boolValue
		case "LogConsumeEnabled":
			common.LogConsumeEnabled = boolValue
		case "DisplayInCurrencyEnabled":
			// 兼容旧字段：同步到新配置 general_setting.quota_display_type（运行时生效）
			// true -> USD, false -> TOKENS
			newVal := "USD"
			if !boolValue {
				newVal = "TOKENS"
			}
			config.GlobalConfig.Update("general_setting", map[string]string{"quota_display_type": newVal})
		case "DisplayTokenStatEnabled":
			common.DisplayTokenStatEnabled = boolValue
		case "DrawingEnabled":
			common.DrawingEnabled = boolValue
		case "TaskEnabled":
			common.TaskEnabled = boolValue
		case "DataExportEnabled":
			common.DataExportEnabled = boolValue
		case "DefaultCollapseSidebar":
			common.DefaultCollapseSidebar = boolValue
		case "MjNotifyEnabled":
			setting.MjNotifyEnabled = boolValue
		case "MjAccountFilterEnabled":
			setting.MjAccountFilterEnabled = boolValue
		case "MjModeClearEnabled":
			setting.MjModeClearEnabled = boolValue
		case "MjForwardUrlEnabled":
			setting.MjForwardUrlEnabled = boolValue
		case "MjActionCheckSuccessEnabled":
			setting.MjActionCheckSuccessEnabled = boolValue
		case "CheckSensitiveEnabled":
			setting.CheckSensitiveEnabled = boolValue
		case "DemoSiteEnabled":
			operation_setting.DemoSiteEnabled = boolValue
		case "SelfUseModeEnabled":
			operation_setting.SelfUseModeEnabled = boolValue
		case "CheckSensitiveOnPromptEnabled":
			setting.CheckSensitiveOnPromptEnabled = boolValue
		case "ModelRequestRateLimitEnabled":
			setting.ModelRequestRateLimitEnabled = boolValue
		case "StopOnSensitiveEnabled":
			setting.StopOnSensitiveEnabled = boolValue
		case "SMTPSSLEnabled":
			common.SMTPSSLEnabled = boolValue
		case "SMTPStartTLSEnabled":
			common.SMTPStartTLSEnabled = boolValue
		case "SMTPInsecureSkipVerify":
			common.SMTPInsecureSkipVerify = boolValue
		case "SMTPForceAuthLogin":
			common.SMTPForceAuthLogin = boolValue
		case "WorkerAllowHttpImageRequestEnabled":
			system_setting.WorkerAllowHttpImageRequestEnabled = boolValue
		case "DefaultUseAutoGroup":
			setting.DefaultUseAutoGroup = boolValue
		case "ExposeRatioEnabled":
			ratio_setting.SetExposeRatioEnabled(boolValue)
		}
	}
	switch key {
	case "EmailDomainWhitelist":
		common.EmailDomainWhitelist = strings.Split(value, ",")
	case "SMTPServer":
		common.SMTPServer = value
	case "SMTPPort":
		intValue, _ := strconv.Atoi(value)
		common.SMTPPort = intValue
	case "SMTPAccount":
		common.SMTPAccount = value
	case "SMTPFrom":
		common.SMTPFrom = value
	case "SMTPToken":
		common.SMTPToken = value
	case "ServerAddress":
		system_setting.ServerAddress = value
	case "WorkerUrl":
		system_setting.WorkerUrl = value
	case "WorkerValidKey":
		system_setting.WorkerValidKey = value
	case "PayAddress":
		operation_setting.PayAddress = value
	case "Chats":
		err = setting.UpdateChatsByJsonString(value)
	case "AutoGroups":
		err = setting.UpdateAutoGroupsByJsonString(value)
	case "CustomCallbackAddress":
		operation_setting.CustomCallbackAddress = value
	case "EpayId":
		operation_setting.EpayId = value
	case "EpayKey":
		operation_setting.EpayKey = value
	case "Price":
		operation_setting.Price, _ = strconv.ParseFloat(value, 64)
	case "USDExchangeRate":
		operation_setting.USDExchangeRate, _ = strconv.ParseFloat(value, 64)
	case "MinTopUp":
		operation_setting.MinTopUp, _ = strconv.Atoi(value)
	case "StripeApiSecret":
		setting.StripeApiSecret = value
	case "StripeWebhookSecret":
		setting.StripeWebhookSecret = value
	case "StripePriceId":
		setting.StripePriceId = value
	case "StripeUnitPrice":
		setting.StripeUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "StripeMinTopUp":
		setting.StripeMinTopUp, _ = strconv.Atoi(value)
	case "StripePromotionCodesEnabled":
		setting.StripePromotionCodesEnabled = value == "true"
	case "CreemApiKey":
		setting.CreemApiKey = value
	case "CreemProducts":
		setting.CreemProducts = value
	case "CreemTestMode":
		setting.CreemTestMode = value == "true"
	case "CreemWebhookSecret":
		setting.CreemWebhookSecret = value
	case "WaffoEnabled":
		setting.WaffoEnabled = value == "true"
	case "WaffoApiKey":
		setting.WaffoApiKey = value
	case "WaffoPrivateKey":
		setting.WaffoPrivateKey = value
	case "WaffoPublicCert":
		setting.WaffoPublicCert = value
	case "WaffoSandboxPublicCert":
		setting.WaffoSandboxPublicCert = value
	case "WaffoSandboxApiKey":
		setting.WaffoSandboxApiKey = value
	case "WaffoSandboxPrivateKey":
		setting.WaffoSandboxPrivateKey = value
	case "WaffoSandbox":
		setting.WaffoSandbox = value == "true"
	case "WaffoMerchantId":
		setting.WaffoMerchantId = value
	case "WaffoNotifyUrl":
		setting.WaffoNotifyUrl = value
	case "WaffoReturnUrl":
		setting.WaffoReturnUrl = value
	case "WaffoSubscriptionReturnUrl":
		setting.WaffoSubscriptionReturnUrl = value
	case "WaffoCurrency":
		setting.WaffoCurrency = value
	case "WaffoUnitPrice":
		setting.WaffoUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "WaffoMinTopUp":
		setting.WaffoMinTopUp, _ = strconv.Atoi(value)
	case "WaffoPancakeMerchantID":
		setting.WaffoPancakeMerchantID = value
	case "WaffoPancakePrivateKey":
		setting.WaffoPancakePrivateKey = value
	case "WaffoPancakeReturnURL":
		setting.WaffoPancakeReturnURL = value
	case "WaffoPancakeStoreID":
		setting.WaffoPancakeStoreID = value
	case "WaffoPancakeProductID":
		setting.WaffoPancakeProductID = value
	case "WaffoPancakeUnitPrice":
		setting.WaffoPancakeUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "WaffoPancakeMinTopUp":
		setting.WaffoPancakeMinTopUp, _ = strconv.Atoi(value)
	case "TopupGroupRatio":
		err = common.UpdateTopupGroupRatioByJSONString(value)
	case "GitHubClientId":
		common.GitHubClientId = value
	case "GitHubClientSecret":
		common.GitHubClientSecret = value
	case "LinuxDOClientId":
		common.LinuxDOClientId = value
	case "LinuxDOClientSecret":
		common.LinuxDOClientSecret = value
	case "LinuxDOMinimumTrustLevel":
		common.LinuxDOMinimumTrustLevel, _ = strconv.Atoi(value)
	case "Footer":
		common.Footer = value
	case "SystemName":
		common.SystemName = value
	case "Logo":
		common.Logo = value
	case "WeChatServerAddress":
		common.WeChatServerAddress = value
	case "WeChatServerToken":
		common.WeChatServerToken = value
	case "WeChatAccountQRCodeImageURL":
		common.WeChatAccountQRCodeImageURL = value
	case "TelegramBotToken":
		common.TelegramBotToken = value
	case "TelegramBotName":
		common.TelegramBotName = value
	case "TurnstileSiteKey":
		common.TurnstileSiteKey = value
	case "TurnstileSecretKey":
		common.TurnstileSecretKey = value
	case "QuotaForNewUser":
		common.QuotaForNewUser, _ = strconv.Atoi(value)
	case "QuotaForInviter":
		common.QuotaForInviter, _ = strconv.Atoi(value)
	case "QuotaForInvitee":
		common.QuotaForInvitee, _ = strconv.Atoi(value)
	case "QuotaRemindThreshold":
		common.QuotaRemindThreshold, _ = strconv.Atoi(value)
	case "PreConsumedQuota":
		common.PreConsumedQuota, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitCount":
		setting.ModelRequestRateLimitCount, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitDurationMinutes":
		setting.ModelRequestRateLimitDurationMinutes, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitSuccessCount":
		setting.ModelRequestRateLimitSuccessCount, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitGroup":
		err = setting.UpdateModelRequestRateLimitGroupByJSONString(value)
	case "RetryTimes":
		common.RetryTimes = retryTimes
	case "AutoDisableTolerance":
		common.AutoDisableTolerance = autoDisableTolerance
	case "DataExportInterval":
		common.DataExportInterval, _ = strconv.Atoi(value)
	case "DataExportDefaultTime":
		common.DataExportDefaultTime = value
	case "ModelRatio":
		err = ratio_setting.UpdateModelRatioByJSONString(value)
	case "GroupRatio":
		err = ratio_setting.UpdateGroupRatioByJSONString(value)
	case "GroupGroupRatio":
		err = ratio_setting.UpdateGroupGroupRatioByJSONString(value)
	case "UserUsableGroups":
		err = setting.UpdateUserUsableGroupsByJSONString(value)
	case "CompletionRatio":
		err = ratio_setting.UpdateCompletionRatioByJSONString(value)
	case "ModelPrice":
		err = ratio_setting.UpdateModelPriceByJSONString(value)
	case "CacheRatio":
		err = ratio_setting.UpdateCacheRatioByJSONString(value)
	case "CreateCacheRatio":
		err = ratio_setting.UpdateCreateCacheRatioByJSONString(value)
	case "ImageRatio":
		err = ratio_setting.UpdateImageRatioByJSONString(value)
	case "AudioRatio":
		err = ratio_setting.UpdateAudioRatioByJSONString(value)
	case "AudioCompletionRatio":
		err = ratio_setting.UpdateAudioCompletionRatioByJSONString(value)
	case "TopUpLink":
		common.TopUpLink = value
	//case "ChatLink":
	//	common.ChatLink = value
	//case "ChatLink2":
	//	common.ChatLink2 = value
	case "ChannelDisableThreshold":
		common.ChannelDisableThreshold, _ = strconv.ParseFloat(value, 64)
	case "QuotaPerUnit":
		common.QuotaPerUnit, _ = strconv.ParseFloat(value, 64)
	case "SensitiveWords":
		setting.SensitiveWordsFromString(value)
	case "AutomaticDisableKeywords":
		operation_setting.AutomaticDisableKeywordsFromString(value)
	case "AutomaticDisableStatusCodes":
		err = operation_setting.AutomaticDisableStatusCodesFromString(value)
	case "AutomaticRetryStatusCodes":
		err = operation_setting.AutomaticRetryStatusCodesFromString(value)
	case "StreamCacheQueueLength":
		setting.StreamCacheQueueLength, _ = strconv.Atoi(value)
	case "PayMethods":
		err = operation_setting.UpdatePayMethodsByJsonString(value)
	case "WaffoPayMethods":
		// WaffoPayMethods is read directly from OptionMap via setting.GetWaffoPayMethods().
		// The value is already stored in OptionMap at the top of this function (line: common.OptionMap[key] = value).
		// No additional in-memory variable to update.
	}
	return err
}

// handleConfigUpdate 处理分层配置更新，返回是否已处理
func handleConfigUpdate(key, value string) bool {
	if key == operation_setting.ToolPriceOptionKey {
		operation_setting.LoadToolPricesFromJSONString(value)
		return true
	}

	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return false // 不是分层配置
	}

	configName := parts[0]
	configKey := parts[1]

	configMap := map[string]string{
		configKey: value,
	}
	if !config.GlobalConfig.Update(configName, configMap) {
		return false // 未注册的配置
	}

	// 特定配置的后处理
	if configName == "performance_setting" {
		performance_setting.UpdateAndSync()
	} else if configName == "billing_setting" {
		InvalidatePricingCache()
		ratio_setting.InvalidateExposedDataCache()
	}

	return true // 已处理
}
