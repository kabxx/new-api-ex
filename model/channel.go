package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Channel struct {
	Id                 int     `json:"id"`
	Type               int     `json:"type" gorm:"default:0"`
	Key                string  `json:"key" gorm:"not null"`
	OpenAIOrganization *string `json:"openai_organization"`
	TestModel          *string `json:"test_model"`
	Status             int     `json:"status" gorm:"default:1"`
	Name               string  `json:"name" gorm:"index"`
	Weight             *uint   `json:"weight" gorm:"default:0"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint"`
	TestTime           int64   `json:"test_time" gorm:"bigint"`
	ResponseTime       int     `json:"response_time"` // in milliseconds
	BaseURL            *string `json:"base_url" gorm:"column:base_url;default:''"`
	Other              string  `json:"other"`
	Balance            float64 `json:"balance"` // in USD
	BalanceUpdatedTime int64   `json:"balance_updated_time" gorm:"bigint"`
	Models             string  `json:"models"`
	Group              string  `json:"group" gorm:"type:varchar(64);default:'default'"`
	UsedQuota          int64   `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping       *string `json:"model_mapping" gorm:"type:text"`
	//MaxInputTokens     *int    `json:"max_input_tokens" gorm:"default:0"`
	StatusCodeMapping *string `json:"status_code_mapping" gorm:"type:varchar(1024);default:''"`
	Priority          *int64  `json:"priority" gorm:"bigint;default:0"`
	AutoBan           *int    `json:"auto_ban" gorm:"default:1"`
	OtherInfo         string  `json:"other_info"`
	Tag               *string `json:"tag" gorm:"index"`
	Setting           *string `json:"setting" gorm:"type:text"` // 渠道额外设置
	ParamOverride     *string `json:"param_override" gorm:"type:text"`
	HeaderOverride    *string `json:"header_override" gorm:"type:text"`
	Remark            *string `json:"remark" gorm:"type:varchar(255)" validate:"max=255"`
	// add after v0.8.5
	ChannelInfo ChannelInfo `json:"channel_info" gorm:"type:json"`

	OtherSettings string `json:"settings" gorm:"column:settings"` // 其他设置，存储azure版本等不需要检索的信息，详见dto.ChannelOtherSettings

	// cache info
	Keys []string `json:"-" gorm:"-"`
}

type ChannelInfo struct {
	IsMultiKey             bool                  `json:"is_multi_key"`                        // 是否多Key模式
	MultiKeySize           int                   `json:"multi_key_size"`                      // 多Key模式下的Key数量
	MultiKeyStatusList     map[int]int           `json:"multi_key_status_list"`               // key状态列表，key index -> status
	MultiKeyDisabledReason map[int]string        `json:"multi_key_disabled_reason,omitempty"` // key禁用原因列表，key index -> reason
	MultiKeyDisabledTime   map[int]int64         `json:"multi_key_disabled_time,omitempty"`   // key禁用时间列表，key index -> time
	MultiKeyPollingIndex   int                   `json:"multi_key_polling_index"`             // 多Key模式下轮询的key索引
	MultiKeyMode           constant.MultiKeyMode `json:"multi_key_mode"`
}

type ChannelSortOptions struct {
	SortBy    string
	SortOrder string
	IDSort    bool
}

var channelSortColumns = map[string]string{
	"id":            "id",
	"name":          "name",
	"priority":      "priority",
	"balance":       "balance",
	"response_time": "response_time",
	"test_time":     "test_time",
}

func NewChannelSortOptions(sortBy string, sortOrder string, idSort bool) ChannelSortOptions {
	normalizedSortBy := strings.ToLower(strings.TrimSpace(sortBy))
	normalizedSortOrder := strings.ToLower(strings.TrimSpace(sortOrder))
	if _, ok := channelSortColumns[normalizedSortBy]; !ok {
		normalizedSortBy = ""
		normalizedSortOrder = ""
	} else if normalizedSortOrder != "asc" {
		normalizedSortOrder = "desc"
	}

	return ChannelSortOptions{
		SortBy:    normalizedSortBy,
		SortOrder: normalizedSortOrder,
		IDSort:    idSort,
	}
}

func (options ChannelSortOptions) Apply(query *gorm.DB) *gorm.DB {
	if columnName, ok := channelSortColumns[options.SortBy]; ok {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: columnName},
			Desc:   options.SortOrder != "asc",
		})
	}
	if options.IDSort {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: "id"},
			Desc:   true,
		})
	}
	return query.Order(clause.OrderByColumn{
		Column: clause.Column{Name: "priority"},
		Desc:   true,
	})
}

func resolveChannelSortOptions(idSort bool, sortOptions []ChannelSortOptions) ChannelSortOptions {
	if len(sortOptions) == 0 {
		return NewChannelSortOptions("", "", idSort)
	}
	options := sortOptions[0]
	options.IDSort = options.IDSort || idSort
	return options
}

func NormalizeChannelGroupFilter(group string) string {
	group = strings.TrimSpace(group)
	if group == "" || strings.EqualFold(group, "all") || strings.EqualFold(group, "null") {
		return ""
	}
	return group
}

func channelGroupFilterCondition() string {
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		return `CONCAT(',', ` + commonGroupCol + `, ',') LIKE ? ESCAPE '!'`
	}
	return `(',' || ` + commonGroupCol + ` || ',') LIKE ? ESCAPE '!'`
}

func channelGroupFilterPattern(group string) string {
	group = strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	).Replace(group)
	return "%," + group + ",%"
}

func ApplyChannelGroupFilter(query *gorm.DB, group string) *gorm.DB {
	group = NormalizeChannelGroupFilter(group)
	if group == "" {
		return query
	}
	return query.Where(channelGroupFilterCondition(), channelGroupFilterPattern(group))
}

// Value implements driver.Valuer interface
func (c ChannelInfo) Value() (driver.Value, error) {
	return common.Marshal(&c)
}

// Scan implements sql.Scanner interface
func (c *ChannelInfo) Scan(value interface{}) error {
	bytesValue, _ := value.([]byte)
	return common.Unmarshal(bytesValue, c)
}

func (channel *Channel) GetKeys() []string {
	if channel.Key == "" {
		return []string{}
	}
	if len(channel.Keys) > 0 {
		return channel.Keys
	}
	trimmed := strings.TrimSpace(channel.Key)
	// If the key starts with '[', try to parse it as a JSON array (e.g., for Vertex AI scenarios)
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := common.Unmarshal([]byte(trimmed), &arr); err == nil {
			res := make([]string, len(arr))
			for i, v := range arr {
				res[i] = string(v)
			}
			return res
		}
	}
	// Otherwise, fall back to splitting by newline
	keys := strings.Split(strings.Trim(channel.Key, "\n"), "\n")
	return keys
}

func (channel *Channel) GetNextEnabledKey() (string, int, *types.NewAPIError) {
	return channel.GetNextEnabledKeyExcluding(nil)
}

func (channel *Channel) GetNextEnabledKeyExcluding(excluded map[int]struct{}) (string, int, *types.NewAPIError) {
	// If not in multi-key mode, return the original key string directly.
	if !channel.ChannelInfo.IsMultiKey {
		if _, skip := excluded[0]; skip {
			return "", 0, types.NewError(errors.New("no untried keys"), types.ErrorCodeChannelNoAvailableKey)
		}
		return channel.Key, 0, nil
	}

	// Obtain all keys (split by \n)
	keys := channel.GetKeys()
	if len(keys) == 0 {
		// No keys available, return error, should disable the channel
		return "", 0, types.NewError(errors.New("no keys available"), types.ErrorCodeChannelNoAvailableKey)
	}

	lock := GetChannelPollingLock(channel.Id)
	lock.Lock()
	defer lock.Unlock()

	statusList := channel.ChannelInfo.MultiKeyStatusList
	// helper to get key status, default to enabled when missing
	getStatus := func(idx int) int {
		if statusList == nil {
			return common.ChannelStatusEnabled
		}
		if status, ok := statusList[idx]; ok {
			return status
		}
		return common.ChannelStatusEnabled
	}

	// Collect indexes of enabled keys
	enabledIdx := make([]int, 0, len(keys))
	for i := range keys {
		_, skip := excluded[i]
		if getStatus(i) == common.ChannelStatusEnabled && !skip {
			enabledIdx = append(enabledIdx, i)
		}
	}
	// If no specific status list or none enabled, return an explicit error so caller can
	// properly handle a channel with no available keys (e.g. mark channel disabled).
	// Returning the first key here caused requests to keep using an already-disabled key.
	if len(enabledIdx) == 0 {
		return "", 0, types.NewError(errors.New("no enabled keys"), types.ErrorCodeChannelNoAvailableKey)
	}

	switch channel.ChannelInfo.MultiKeyMode {
	case constant.MultiKeyModeRandom:
		// Randomly pick one enabled key
		selectedIdx := enabledIdx[rand.Intn(len(enabledIdx))]
		return keys[selectedIdx], selectedIdx, nil
	case constant.MultiKeyModePolling:
		// Use channel-specific lock to ensure thread-safe polling

		channelInfo, err := CacheGetChannelInfo(channel.Id)
		if err != nil {
			return "", 0, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		defer func() {
			if common.DebugEnabled {
				logger.LogDebug(nil, "channel %d polling index: %d", channel.Id, channel.ChannelInfo.MultiKeyPollingIndex)
			}
			if !common.MemoryCacheEnabled {
				_ = channel.SaveChannelInfo()
			} else {
				// CacheUpdateChannel(channel)
			}
		}()
		// Start from the saved polling index and look for the next enabled key
		start := channelInfo.MultiKeyPollingIndex
		if start < 0 || start >= len(keys) {
			start = 0
		}
		for i := 0; i < len(keys); i++ {
			idx := (start + i) % len(keys)
			_, skip := excluded[idx]
			if getStatus(idx) == common.ChannelStatusEnabled && !skip {
				// update polling index for next call (point to the next position)
				channel.ChannelInfo.MultiKeyPollingIndex = (idx + 1) % len(keys)
				return keys[idx], idx, nil
			}
		}
		// Fallback – should not happen, but return first enabled key
		return keys[enabledIdx[0]], enabledIdx[0], nil
	default:
		// Unknown mode, default to first enabled key (or original key string)
		return keys[enabledIdx[0]], enabledIdx[0], nil
	}
}

func (channel *Channel) HasEnabledKeyExcluding(excluded map[int]struct{}) bool {
	if channel == nil {
		return false
	}
	if !channel.ChannelInfo.IsMultiKey {
		_, skip := excluded[0]
		return !skip
	}
	lock := GetChannelPollingLock(channel.Id)
	lock.Lock()
	defer lock.Unlock()
	keys := channel.GetKeys()
	for index := range keys {
		if _, skip := excluded[index]; skip {
			continue
		}
		status := common.ChannelStatusEnabled
		if channel.ChannelInfo.MultiKeyStatusList != nil {
			if stored, ok := channel.ChannelInfo.MultiKeyStatusList[index]; ok {
				status = stored
			}
		}
		if status == common.ChannelStatusEnabled {
			return true
		}
	}
	return false
}

func (channel *Channel) SaveChannelInfo() error {
	return DB.Model(channel).Update("channel_info", channel.ChannelInfo).Error
}

func (channel *Channel) GetModels() []string {
	if channel.Models == "" {
		return []string{}
	}
	return strings.Split(strings.Trim(channel.Models, ","), ",")
}

func (channel *Channel) GetGroups() []string {
	if channel.Group == "" {
		return []string{}
	}
	groups := strings.Split(strings.Trim(channel.Group, ","), ",")
	for i, group := range groups {
		groups[i] = strings.TrimSpace(group)
	}
	return groups
}

func (channel *Channel) GetOtherInfo() map[string]interface{} {
	otherInfo := make(map[string]interface{})
	if channel.OtherInfo != "" {
		err := common.Unmarshal([]byte(channel.OtherInfo), &otherInfo)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal other info: channel_id=%d, tag=%s, name=%s, error=%v", channel.Id, channel.GetTag(), channel.Name, err))
		}
	}
	return otherInfo
}

func (channel *Channel) SetOtherInfo(otherInfo map[string]interface{}) {
	otherInfoBytes, err := json.Marshal(otherInfo)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal other info: channel_id=%d, tag=%s, name=%s, error=%v", channel.Id, channel.GetTag(), channel.Name, err))
		return
	}
	channel.OtherInfo = string(otherInfoBytes)
}

func (channel *Channel) GetTag() string {
	if channel.Tag == nil {
		return ""
	}
	return *channel.Tag
}

func (channel *Channel) SetTag(tag string) {
	channel.Tag = &tag
}

func (channel *Channel) GetAutoBan() bool {
	if channel.AutoBan == nil {
		return false
	}
	return *channel.AutoBan == 1
}

func (channel *Channel) Save() error {
	return DB.Save(channel).Error
}

func (channel *Channel) SaveWithoutKey() error {
	if channel.Id == 0 {
		return errors.New("channel ID is 0")
	}
	return DB.Omit("key").Save(channel).Error
}

func GetAllChannels(startIdx int, num int, selectAll bool, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	var err error
	order := resolveChannelSortOptions(idSort, sortOptions)
	if selectAll {
		err = order.Apply(DB).Find(&channels).Error
	} else {
		err = order.Apply(DB).Limit(num).Offset(startIdx).Omit("key").Find(&channels).Error
	}
	return channels, err
}

func GetChannelsByTag(tag string, idSort bool, selectAll bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	order := resolveChannelSortOptions(idSort, sortOptions)
	query := order.Apply(DB.Where("tag = ?", tag))
	if !selectAll {
		query = query.Omit("key")
	}
	err := query.Find(&channels).Error
	return channels, err
}

func SearchChannels(keyword string, group string, model string, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	modelsCol := "`models`"

	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		modelsCol = `"models"`
	}

	baseURLCol := "`base_url`"
	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		baseURLCol = `"base_url"`
	}

	order := resolveChannelSortOptions(idSort, sortOptions)

	// 构造基础查询
	baseQuery := DB.Model(&Channel{}).Omit("key")

	// 构造WHERE子句
	whereClause := "(id = ? OR name LIKE ? OR " + commonKeyCol + " = ? OR " + baseURLCol + " LIKE ?) AND " + modelsCol + " LIKE ?"
	args := []any{common.String2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%", "%" + model + "%"}
	baseQuery = ApplyChannelGroupFilter(baseQuery.Where(whereClause, args...), group)

	// 执行查询
	err := order.Apply(baseQuery).Find(&channels).Error
	if err != nil {
		return nil, err
	}
	return channels, nil
}

func GetChannelById(id int, selectAll bool) (*Channel, error) {
	channel := &Channel{Id: id}
	var err error = nil
	if selectAll {
		err = DB.First(channel, "id = ?", id).Error
	} else {
		err = DB.Omit("key").First(channel, "id = ?", id).Error
	}
	if err != nil {
		return nil, err
	}
	return channel, nil
}

func BatchInsertChannels(channels []Channel) error {
	if len(channels) == 0 {
		return nil
	}
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for _, chunk := range lo.Chunk(channels, 50) {
		if err := tx.Create(&chunk).Error; err != nil {
			tx.Rollback()
			return err
		}
		for _, channel_ := range chunk {
			if err := channel_.AddAbilities(tx); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit().Error
}

func BatchDeleteChannels(ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// 使用事务 分批删除channel表和abilities表
	tx := DB.Begin()
	if tx.Error != nil {
		return 0, tx.Error
	}
	var deletedCount int64
	for _, chunk := range lo.Chunk(ids, 200) {
		result := tx.Where("id in (?)", chunk).Delete(&Channel{})
		if result.Error != nil {
			tx.Rollback()
			return 0, result.Error
		}
		deletedCount += result.RowsAffected
		if err := tx.Where("channel_id in (?)", chunk).Delete(&Ability{}).Error; err != nil {
			tx.Rollback()
			return 0, err
		}
		if tx.Migrator().HasTable(&ChannelFailureState{}) {
			if err := tx.Where("channel_id in (?)", chunk).Delete(&ChannelFailureState{}).Error; err != nil {
				tx.Rollback()
				return 0, err
			}
		}
		if tx.Migrator().HasTable(&ChannelSelectionMetricState{}) {
			if err := tx.Where("channel_id in (?)", chunk).Delete(&ChannelSelectionMetricState{}).Error; err != nil {
				tx.Rollback()
				return 0, err
			}
		}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, err
	}
	clearChannelSelectionMetricsForChannels(ids)
	return deletedCount, nil
}

func (channel *Channel) GetPriority() int64 {
	if channel.Priority == nil {
		return 0
	}
	return *channel.Priority
}

func (channel *Channel) GetWeight() int {
	if channel.Weight == nil {
		return 0
	}
	return int(*channel.Weight)
}

func (channel *Channel) GetBaseURL() string {
	if channel.BaseURL == nil {
		return ""
	}
	url := *channel.BaseURL
	if url == "" {
		url = constant.ChannelBaseURLs[channel.Type]
	}
	return url
}

func (channel *Channel) GetModelMapping() string {
	if channel.ModelMapping == nil {
		return ""
	}
	return *channel.ModelMapping
}

func (channel *Channel) GetStatusCodeMapping() string {
	if channel.StatusCodeMapping == nil {
		return ""
	}
	return *channel.StatusCodeMapping
}

func (channel *Channel) Insert() error {
	var err error
	err = DB.Create(channel).Error
	if err != nil {
		return err
	}
	err = channel.AddAbilities(nil)
	return err
}

func (channel *Channel) Update() error {
	// If this is a multi-key channel, recalculate MultiKeySize based on the current key list to avoid inconsistency after editing keys
	if channel.ChannelInfo.IsMultiKey {
		if channel.Status == 0 {
			if existing, err := GetChannelById(channel.Id, false); err == nil {
				channel.Status = existing.Status
			}
		}
		var keyStr string
		if channel.Key != "" {
			keyStr = channel.Key
		} else {
			// If key is not provided, read the existing key from the database
			if existing, err := GetChannelById(channel.Id, true); err == nil {
				keyStr = existing.Key
			}
		}
		// Parse the key list (supports newline separation or JSON array)
		keys := []string{}
		if keyStr != "" {
			trimmed := strings.TrimSpace(keyStr)
			if strings.HasPrefix(trimmed, "[") {
				var arr []json.RawMessage
				if err := common.Unmarshal([]byte(trimmed), &arr); err == nil {
					keys = make([]string, len(arr))
					for i, v := range arr {
						keys[i] = string(v)
					}
				}
			}
			if len(keys) == 0 { // fallback to newline split
				keys = strings.Split(strings.Trim(keyStr, "\n"), "\n")
			}
		}
		channel.ChannelInfo.MultiKeySize = len(keys)
		// Clean up status data that exceeds the new key count to prevent index out of range
		if channel.ChannelInfo.MultiKeyStatusList != nil {
			for idx := range channel.ChannelInfo.MultiKeyStatusList {
				if idx >= channel.ChannelInfo.MultiKeySize {
					delete(channel.ChannelInfo.MultiKeyStatusList, idx)
				}
			}
		}
		if channel.Status != common.ChannelStatusManuallyDisabled {
			if !hasEnabledMultiKey(keys, channel.ChannelInfo.MultiKeyStatusList) {
				channel.Status = common.ChannelStatusAutoDisabled
			}
		}
	}
	var err error
	err = DB.Model(channel).Updates(channel).Error
	if err != nil {
		return err
	}
	DB.Model(channel).First(channel, "id = ?", channel.Id)
	err = channel.UpdateAbilities(nil)
	return err
}

func (channel *Channel) UpdateResponseTime(responseTime int64) {
	err := DB.Model(channel).Select("response_time", "test_time").Updates(Channel{
		TestTime:     common.GetTimestamp(),
		ResponseTime: int(responseTime),
	}).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update response time: channel_id=%d, error=%v", channel.Id, err))
	}
}

func (channel *Channel) UpdateBalance(balance float64) {
	err := DB.Model(channel).Select("balance_updated_time", "balance").Updates(Channel{
		BalanceUpdatedTime: common.GetTimestamp(),
		Balance:            balance,
	}).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update balance: channel_id=%d, error=%v", channel.Id, err))
	}
}

func (channel *Channel) Delete() error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(channel).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error; err != nil {
			return err
		}
		if err := deleteChannelFailureStatesTx(tx, channel.Id); err != nil {
			return err
		}
		if tx.Migrator().HasTable(&ChannelSelectionMetricState{}) {
			if err := tx.Where("channel_id = ?", channel.Id).Delete(&ChannelSelectionMetricState{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		clearChannelSelectionMetricsForChannels([]int{channel.Id})
	}
	return err
}

var channelStatusLock sync.Mutex

// channelPollingLocks stores locks for each channel.id to ensure thread-safe polling
var channelPollingLocks sync.Map

// GetChannelPollingLock returns or creates a mutex for the given channel ID
func GetChannelPollingLock(channelId int) *sync.Mutex {
	if lock, exists := channelPollingLocks.Load(channelId); exists {
		return lock.(*sync.Mutex)
	}
	// Create new lock for this channel
	newLock := &sync.Mutex{}
	actual, _ := channelPollingLocks.LoadOrStore(channelId, newLock)
	return actual.(*sync.Mutex)
}

// CleanupChannelPollingLocks removes locks for channels that no longer exist
// This is optional and can be called periodically to prevent memory leaks
func CleanupChannelPollingLocks() {
	var activeChannelIds []int
	DB.Model(&Channel{}).Pluck("id", &activeChannelIds)

	activeChannelSet := make(map[int]bool)
	for _, id := range activeChannelIds {
		activeChannelSet[id] = true
	}

	channelPollingLocks.Range(func(key, value interface{}) bool {
		channelId := key.(int)
		if !activeChannelSet[channelId] {
			channelPollingLocks.Delete(channelId)
		}
		return true
	})
}

type ChannelStatusMutation struct {
	KeyChanged     bool
	ChannelChanged bool
	PreviousStatus int
	CurrentStatus  int
}

func (mutation ChannelStatusMutation) Changed() bool {
	return mutation.KeyChanged || mutation.ChannelChanged
}

var (
	ErrChannelAttemptDisabled = errors.New("channel is disabled")
	ErrChannelKeyDisabled     = errors.New("channel key is disabled")
	ErrChannelKeyChanged      = errors.New("channel key selection is stale")
)

func findChannelKeyIndex(channel *Channel, usingKey string) int {
	for index, key := range channel.GetKeys() {
		if key == usingKey {
			return index
		}
	}
	return -1
}

func setChannelStatusReason(channel *Channel, reason string) {
	info := channel.GetOtherInfo()
	info["status_reason"] = reason
	info["status_time"] = common.GetTimestamp()
	channel.SetOtherInfo(info)
}

func handlerMultiKeyUpdate(channel *Channel, usingKey string, status int, reason string) bool {
	keys := channel.GetKeys()
	if len(keys) == 0 {
		if channel.Status == status {
			return false
		}
		channel.Status = status
		setChannelStatusReason(channel, reason)
		return true
	} else {
		keyIndex := findChannelKeyIndex(channel, usingKey)
		if keyIndex < 0 {
			if usingKey != "" {
				common.SysLog(fmt.Sprintf("failed to update multi-key status: channel_id=%d, using key not found", channel.Id))
				return false
			}
			if channel.Status == status {
				return false
			}
			channel.Status = status
			setChannelStatusReason(channel, reason)
			return true
		}
		if channel.ChannelInfo.MultiKeyStatusList == nil {
			channel.ChannelInfo.MultiKeyStatusList = make(map[int]int)
		}
		if status == common.ChannelStatusEnabled {
			if _, exists := channel.ChannelInfo.MultiKeyStatusList[keyIndex]; !exists {
				return false
			}
			delete(channel.ChannelInfo.MultiKeyStatusList, keyIndex)
		} else {
			if channel.ChannelInfo.MultiKeyStatusList[keyIndex] == status {
				return false
			}
			channel.ChannelInfo.MultiKeyStatusList[keyIndex] = status
			if channel.ChannelInfo.MultiKeyDisabledReason == nil {
				channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string)
			}
			if channel.ChannelInfo.MultiKeyDisabledTime == nil {
				channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64)
			}
			channel.ChannelInfo.MultiKeyDisabledReason[keyIndex] = reason
			channel.ChannelInfo.MultiKeyDisabledTime[keyIndex] = common.GetTimestamp()
		}
		if !hasEnabledMultiKey(keys, channel.ChannelInfo.MultiKeyStatusList) {
			channel.Status = common.ChannelStatusAutoDisabled
			setChannelStatusReason(channel, "All keys are disabled")
		} else if status == common.ChannelStatusEnabled {
			channel.Status = common.ChannelStatusEnabled
		}
		return true
	}
}

func hasEnabledMultiKey(keys []string, statusList map[int]int) bool {
	for i := range keys {
		if statusList == nil {
			return true
		}
		status, ok := statusList[i]
		if !ok || status == common.ChannelStatusEnabled {
			return true
		}
	}
	return false
}

func persistChannelStatusMutation(tx *gorm.DB, channel *Channel, channelChanged bool) error {
	if err := tx.Model(&Channel{}).
		Where("id = ?", channel.Id).
		Select("key", "status", "channel_info", "other_info").
		Updates(channel).Error; err != nil {
		return err
	}
	if !channelChanged {
		return nil
	}
	return tx.Model(&Ability{}).
		Where("channel_id = ?", channel.Id).
		Select("enabled").
		Update("enabled", channel.Status == common.ChannelStatusEnabled).Error
}

func refreshChannelStatusCache(channel *Channel, channelChanged bool) {
	if !common.MemoryCacheEnabled || channel == nil {
		return
	}
	if channelChanged {
		InitChannelCache()
		return
	}
	CacheUpdateChannel(channel)
}

func UpdateChannelStatus(channelId int, usingKey string, status int, reason string) bool {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	var updated *Channel
	changed := false
	channelChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel := &Channel{}
		if err := lockForUpdate(tx).First(channel, "id = ?", channelId).Error; err != nil {
			return err
		}
		previousStatus := channel.Status
		if channel.ChannelInfo.IsMultiKey {
			changed = handlerMultiKeyUpdate(channel, usingKey, status, reason)
		} else if channel.Status != status {
			channel.Status = status
			setChannelStatusReason(channel, reason)
			changed = true
		}
		if !changed {
			return nil
		}
		channelChanged = previousStatus != channel.Status
		if err := persistChannelStatusMutation(tx, channel, channelChanged); err != nil {
			return err
		}
		updated = channel
		return nil
	})
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update channel status: channel_id=%d, status=%d, error=%v", channelId, status, err))
		return false
	}
	refreshChannelStatusCache(updated, channelChanged)
	return changed
}

// ApplyManualChannelStatus performs a whole-channel manual state change and
// clears policy evidence in the same transaction. Automatic transitions must
// use ApplyAutomaticChannelStatus instead.
func ApplyManualChannelStatus(channelId int, status int, reason string) (ChannelStatusMutation, error) {
	if DB == nil {
		return ChannelStatusMutation{}, errors.New("main database is unavailable")
	}
	if status != common.ChannelStatusEnabled && status != common.ChannelStatusManuallyDisabled {
		return ChannelStatusMutation{}, fmt.Errorf("invalid manual channel status: %d", status)
	}
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	mutation := ChannelStatusMutation{}
	var updated *Channel
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel := &Channel{}
		if err := lockForUpdate(tx).First(channel, "id = ?", channelId).Error; err != nil {
			return err
		}
		mutation.PreviousStatus = channel.Status
		mutation.CurrentStatus = channel.Status
		if channel.Status != status {
			channel.Status = status
			setChannelStatusReason(channel, reason)
			mutation.ChannelChanged = true
			mutation.CurrentStatus = status
			if err := persistChannelStatusMutation(tx, channel, true); err != nil {
				return err
			}
			updated = channel
		}
		if err := deleteChannelFailureStatesTx(tx, channelId); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ChannelStatusMutation{}, err
	}
	refreshChannelStatusCache(updated, mutation.ChannelChanged)
	return mutation, nil
}

// ApplyManualChannelStatuses atomically applies one manual state to a set of
// channels and clears their policy evidence before the transaction commits.
func ApplyManualChannelStatuses(channelIDs []int, status int, reason string) (int, error) {
	if DB == nil {
		return 0, errors.New("main database is unavailable")
	}
	if status != common.ChannelStatusEnabled && status != common.ChannelStatusManuallyDisabled {
		return 0, fmt.Errorf("invalid manual channel status: %d", status)
	}
	if len(channelIDs) == 0 {
		return 0, nil
	}
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	changed := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, channelID := range channelIDs {
			channel := &Channel{}
			if err := lockForUpdate(tx).First(channel, "id = ?", channelID).Error; err != nil {
				return err
			}
			if channel.Status != status {
				channel.Status = status
				setChannelStatusReason(channel, reason)
				if err := persistChannelStatusMutation(tx, channel, true); err != nil {
					return err
				}
				changed++
			}
			if err := deleteChannelFailureStatesTx(tx, channelID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if changed > 0 && common.MemoryCacheEnabled {
		InitChannelCache()
	}
	return changed, nil
}

type ManualMultiKeyMutation struct {
	ChannelStatusMutation
	ChangedKeys int
}

// ApplyManualMultiKeyMutation serializes all administrator key edits with
// automatic status transitions and resets policy evidence transactionally.
func ApplyManualMultiKeyMutation(channelID int, action string, keyIndex *int) (ManualMultiKeyMutation, error) {
	if DB == nil {
		return ManualMultiKeyMutation{}, errors.New("main database is unavailable")
	}
	pollingLock := GetChannelPollingLock(channelID)
	pollingLock.Lock()
	defer pollingLock.Unlock()
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	mutation := ManualMultiKeyMutation{}
	var updated *Channel
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel := &Channel{}
		if err := lockForUpdate(tx).First(channel, "id = ?", channelID).Error; err != nil {
			return err
		}
		if !channel.ChannelInfo.IsMultiKey {
			return errors.New("channel is not in multi-key mode")
		}
		keys := channel.GetKeys()
		if keyIndex != nil && (*keyIndex < 0 || *keyIndex >= len(keys)) {
			return errors.New("key index is out of range")
		}
		if (action == "disable_key" || action == "enable_key" || action == "delete_key") && keyIndex == nil {
			return errors.New("key index is required")
		}
		if channel.ChannelInfo.MultiKeyStatusList == nil {
			channel.ChannelInfo.MultiKeyStatusList = make(map[int]int)
		}
		if channel.ChannelInfo.MultiKeyDisabledTime == nil {
			channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64)
		}
		if channel.ChannelInfo.MultiKeyDisabledReason == nil {
			channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string)
		}
		previousStatus := channel.Status
		changedKeys := 0
		manualReason := "manual key operation"
		setManualDisabled := func(index int) {
			if channel.ChannelInfo.MultiKeyStatusList[index] == common.ChannelStatusManuallyDisabled {
				return
			}
			channel.ChannelInfo.MultiKeyStatusList[index] = common.ChannelStatusManuallyDisabled
			channel.ChannelInfo.MultiKeyDisabledTime[index] = common.GetTimestamp()
			channel.ChannelInfo.MultiKeyDisabledReason[index] = manualReason
			changedKeys++
		}
		clearKeyStatus := func(index int) {
			if _, exists := channel.ChannelInfo.MultiKeyStatusList[index]; !exists {
				return
			}
			delete(channel.ChannelInfo.MultiKeyStatusList, index)
			delete(channel.ChannelInfo.MultiKeyDisabledTime, index)
			delete(channel.ChannelInfo.MultiKeyDisabledReason, index)
			changedKeys++
		}

		switch action {
		case "disable_key":
			setManualDisabled(*keyIndex)
		case "enable_key":
			clearKeyStatus(*keyIndex)
		case "enable_all_keys":
			for index := range channel.ChannelInfo.MultiKeyStatusList {
				clearKeyStatus(index)
			}
		case "disable_all_keys":
			for index := range keys {
				status := channel.ChannelInfo.MultiKeyStatusList[index]
				if status == 0 || status == common.ChannelStatusEnabled {
					setManualDisabled(index)
				}
			}
		case "delete_key":
			if len(keys) <= 1 {
				return errors.New("cannot delete the last key")
			}
			removed := keys[*keyIndex]
			remaining := make([]string, 0, len(keys)-1)
			statusList := make(map[int]int)
			disabledTime := make(map[int]int64)
			disabledReason := make(map[int]string)
			newIndex := 0
			for index, key := range keys {
				if index == *keyIndex {
					continue
				}
				remaining = append(remaining, key)
				if status, ok := channel.ChannelInfo.MultiKeyStatusList[index]; ok {
					statusList[newIndex] = status
					if value, ok := channel.ChannelInfo.MultiKeyDisabledTime[index]; ok {
						disabledTime[newIndex] = value
					}
					if value, ok := channel.ChannelInfo.MultiKeyDisabledReason[index]; ok {
						disabledReason[newIndex] = value
					}
				}
				newIndex++
			}
			channel.Key = strings.Join(remaining, "\n")
			channel.ChannelInfo.MultiKeySize = len(remaining)
			channel.ChannelInfo.MultiKeyStatusList = statusList
			channel.ChannelInfo.MultiKeyDisabledTime = disabledTime
			channel.ChannelInfo.MultiKeyDisabledReason = disabledReason
			changedKeys = 1
			if err := deleteChannelFailureStatesForKeysTx(tx, channelID, []string{removed}); err != nil {
				return err
			}
		case "delete_disabled_keys":
			remaining := make([]string, 0, len(keys))
			statusList := make(map[int]int)
			disabledTime := make(map[int]int64)
			disabledReason := make(map[int]string)
			newIndex := 0
			for index, key := range keys {
				if channel.ChannelInfo.MultiKeyStatusList[index] == common.ChannelStatusAutoDisabled {
					changedKeys++
					continue
				}
				remaining = append(remaining, key)
				if status, ok := channel.ChannelInfo.MultiKeyStatusList[index]; ok {
					statusList[newIndex] = status
				}
				if value, ok := channel.ChannelInfo.MultiKeyDisabledTime[index]; ok {
					disabledTime[newIndex] = value
				}
				if value, ok := channel.ChannelInfo.MultiKeyDisabledReason[index]; ok {
					disabledReason[newIndex] = value
				}
				newIndex++
			}
			if changedKeys == 0 {
				return nil
			}
			channel.Key = strings.Join(remaining, "\n")
			channel.ChannelInfo.MultiKeySize = len(remaining)
			channel.ChannelInfo.MultiKeyStatusList = statusList
			channel.ChannelInfo.MultiKeyDisabledTime = disabledTime
			channel.ChannelInfo.MultiKeyDisabledReason = disabledReason
		default:
			return fmt.Errorf("unsupported multi-key action: %s", action)
		}

		if changedKeys == 0 {
			if keyIndex != nil {
				return deleteChannelFailureStatesForKeysTx(tx, channelID, []string{keys[*keyIndex]})
			}
			return deleteChannelFailureStatesTx(tx, channelID)
		}
		if channel.Status != common.ChannelStatusManuallyDisabled {
			if hasEnabledMultiKey(channel.GetKeys(), channel.ChannelInfo.MultiKeyStatusList) {
				if channel.Status == common.ChannelStatusAutoDisabled {
					channel.Status = common.ChannelStatusEnabled
				}
			} else {
				channel.Status = common.ChannelStatusAutoDisabled
				setChannelStatusReason(channel, "All keys are disabled")
			}
		}
		mutation.PreviousStatus = previousStatus
		mutation.CurrentStatus = channel.Status
		mutation.ChannelChanged = previousStatus != channel.Status
		mutation.KeyChanged = changedKeys > 0
		mutation.ChangedKeys = changedKeys
		if err := persistChannelStatusMutation(tx, channel, mutation.ChannelChanged); err != nil {
			return err
		}
		if action != "delete_key" && action != "delete_disabled_keys" {
			if err := deleteChannelFailureStatesTx(tx, channelID); err != nil {
				return err
			}
		} else if action == "delete_disabled_keys" {
			if err := deleteChannelFailureStatesTx(tx, channelID); err != nil {
				return err
			}
		}
		if mutation.KeyChanged {
			if err := deleteChannelSelectionMetricsTx(tx, channelID); err != nil {
				return err
			}
		}
		updated = channel
		return nil
	})
	if err != nil {
		return ManualMultiKeyMutation{}, err
	}
	if mutation.KeyChanged {
		clearChannelSelectionMetricsForChannels([]int{channelID})
	}
	refreshChannelStatusCache(updated, mutation.ChannelChanged)
	return mutation, nil
}

func ApplyAutomaticChannelStatus(channelId int, usingKey string, status int, reason string) (ChannelStatusMutation, error) {
	return applyAutomaticChannelStatus(channelId, usingKey, status, reason, "")
}

func ApplyAutomaticChannelStatusWithClaim(channelId int, usingKey string, status int, reason string, claimToken string) (ChannelStatusMutation, error) {
	if claimToken == "" {
		return ChannelStatusMutation{}, errors.New("channel failure claim token is required")
	}
	if status != common.ChannelStatusAutoDisabled {
		return ChannelStatusMutation{}, errors.New("channel failure claims can only auto-disable a channel")
	}
	return applyAutomaticChannelStatus(channelId, usingKey, status, reason, claimToken)
}

func applyAutomaticChannelStatus(channelId int, usingKey string, status int, reason string, claimToken string) (ChannelStatusMutation, error) {
	if DB == nil {
		return ChannelStatusMutation{}, errors.New("main database is unavailable")
	}
	if status != common.ChannelStatusAutoDisabled && status != common.ChannelStatusEnabled {
		return ChannelStatusMutation{}, fmt.Errorf("invalid automatic channel status: %d", status)
	}
	pollingLock := GetChannelPollingLock(channelId)
	pollingLock.Lock()
	defer pollingLock.Unlock()
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	mutation := ChannelStatusMutation{}
	var updated *Channel
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel := &Channel{}
		if err := lockForUpdate(tx).First(channel, "id = ?", channelId).Error; err != nil {
			return err
		}
		if claimToken != "" {
			claimActive, err := validPersistentChannelFailureClaimTx(
				tx,
				channelId,
				ChannelFailureKeyHash(usingKey),
				claimToken,
				common.GetTimestamp(),
			)
			if err != nil {
				return err
			}
			if !claimActive {
				return nil
			}
		}
		mutation.PreviousStatus = channel.Status
		mutation.CurrentStatus = channel.Status
		if status == common.ChannelStatusAutoDisabled && !channel.GetAutoBan() {
			return nil
		}

		if !channel.ChannelInfo.IsMultiKey || usingKey == "" {
			expected := common.ChannelStatusEnabled
			if status == common.ChannelStatusEnabled {
				expected = common.ChannelStatusAutoDisabled
			}
			if channel.Status != expected {
				return nil
			}
			channel.Status = status
			setChannelStatusReason(channel, reason)
			mutation.ChannelChanged = true
		} else {
			keyIndexes := make([]int, 0, 1)
			for index, key := range channel.GetKeys() {
				if key == usingKey {
					keyIndexes = append(keyIndexes, index)
				}
			}
			if len(keyIndexes) == 0 {
				return ErrChannelKeyChanged
			}
			if channel.ChannelInfo.MultiKeyStatusList == nil {
				channel.ChannelInfo.MultiKeyStatusList = make(map[int]int)
			}
			if status == common.ChannelStatusAutoDisabled {
				if channel.ChannelInfo.MultiKeyDisabledReason == nil {
					channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string)
				}
				if channel.ChannelInfo.MultiKeyDisabledTime == nil {
					channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64)
				}
				for _, keyIndex := range keyIndexes {
					keyStatus := common.ChannelStatusEnabled
					if stored, ok := channel.ChannelInfo.MultiKeyStatusList[keyIndex]; ok {
						keyStatus = stored
					}
					if keyStatus != common.ChannelStatusEnabled {
						continue
					}
					channel.ChannelInfo.MultiKeyStatusList[keyIndex] = common.ChannelStatusAutoDisabled
					channel.ChannelInfo.MultiKeyDisabledReason[keyIndex] = reason
					channel.ChannelInfo.MultiKeyDisabledTime[keyIndex] = common.GetTimestamp()
					mutation.KeyChanged = true
				}
				if channel.Status == common.ChannelStatusEnabled && !hasEnabledMultiKey(channel.GetKeys(), channel.ChannelInfo.MultiKeyStatusList) {
					channel.Status = common.ChannelStatusAutoDisabled
					setChannelStatusReason(channel, "All keys are disabled")
					mutation.ChannelChanged = true
				}
			} else {
				for _, keyIndex := range keyIndexes {
					if channel.ChannelInfo.MultiKeyStatusList[keyIndex] != common.ChannelStatusAutoDisabled {
						continue
					}
					delete(channel.ChannelInfo.MultiKeyStatusList, keyIndex)
					delete(channel.ChannelInfo.MultiKeyDisabledReason, keyIndex)
					delete(channel.ChannelInfo.MultiKeyDisabledTime, keyIndex)
					mutation.KeyChanged = true
				}
				if mutation.KeyChanged && channel.Status == common.ChannelStatusAutoDisabled && hasEnabledMultiKey(channel.GetKeys(), channel.ChannelInfo.MultiKeyStatusList) {
					channel.Status = common.ChannelStatusEnabled
					setChannelStatusReason(channel, "")
					mutation.ChannelChanged = true
				}
			}
		}

		mutation.CurrentStatus = channel.Status
		if !mutation.Changed() {
			return nil
		}
		if err := persistChannelStatusMutation(tx, channel, mutation.ChannelChanged); err != nil {
			return err
		}
		updated = channel
		return nil
	})
	if err != nil {
		return ChannelStatusMutation{}, err
	}
	refreshChannelStatusCache(updated, mutation.ChannelChanged)
	return mutation, nil
}

func ValidateChannelAttempt(channelId int, keyIndex int, usingKey string) (*Channel, error) {
	channel, err := GetChannelById(channelId, true)
	if err != nil {
		return nil, err
	}
	if channel.Status != common.ChannelStatusEnabled {
		return nil, ErrChannelAttemptDisabled
	}
	if !channel.ChannelInfo.IsMultiKey {
		if usingKey != "" && channel.Key != usingKey {
			return nil, ErrChannelKeyChanged
		}
		return channel, nil
	}
	keys := channel.GetKeys()
	if keyIndex < 0 || keyIndex >= len(keys) || (usingKey != "" && keys[keyIndex] != usingKey) {
		return nil, ErrChannelKeyChanged
	}
	if channel.ChannelInfo.MultiKeyStatusList != nil {
		if status, ok := channel.ChannelInfo.MultiKeyStatusList[keyIndex]; ok && status != common.ChannelStatusEnabled {
			return nil, ErrChannelKeyDisabled
		}
	}
	return channel, nil
}

func EnableChannelByTag(tag string) error {
	var ids []int
	if err := DB.Model(&Channel{}).Where("tag = ?", tag).Pluck("id", &ids).Error; err != nil {
		return err
	}
	_, err := ApplyManualChannelStatuses(ids, common.ChannelStatusEnabled, "manual tag operation")
	return err
}

func DisableChannelByTag(tag string) error {
	var ids []int
	if err := DB.Model(&Channel{}).Where("tag = ?", tag).Pluck("id", &ids).Error; err != nil {
		return err
	}
	_, err := ApplyManualChannelStatuses(ids, common.ChannelStatusManuallyDisabled, "manual tag operation")
	return err
}

func EditChannelByTag(tag string, newTag *string, modelMapping *string, models *string, group *string, priority *int64, weight *uint, paramOverride *string, headerOverride *string) error {
	updateData := Channel{}
	shouldReCreateAbilities := false
	updatedTag := tag
	// 如果 newTag 不为空且不等于 tag，则更新 tag
	if newTag != nil && *newTag != tag {
		updateData.Tag = newTag
		updatedTag = *newTag
	}
	if modelMapping != nil {
		updateData.ModelMapping = modelMapping
	}
	if models != nil && *models != "" {
		shouldReCreateAbilities = true
		updateData.Models = *models
	}
	if group != nil && *group != "" {
		shouldReCreateAbilities = true
		updateData.Group = *group
	}
	if priority != nil {
		updateData.Priority = priority
	}
	if weight != nil {
		updateData.Weight = weight
	}
	if paramOverride != nil {
		updateData.ParamOverride = paramOverride
	}
	if headerOverride != nil {
		updateData.HeaderOverride = headerOverride
	}

	err := DB.Model(&Channel{}).Where("tag = ?", tag).Updates(updateData).Error
	if err != nil {
		return err
	}
	if shouldReCreateAbilities {
		channels, err := GetChannelsByTag(updatedTag, false, false)
		if err == nil {
			for _, channel := range channels {
				err = channel.UpdateAbilities(nil)
				if err != nil {
					common.SysLog(fmt.Sprintf("failed to update abilities: channel_id=%d, tag=%s, error=%v", channel.Id, channel.GetTag(), err))
				}
			}
		}
	} else {
		err := UpdateAbilityByTag(tag, newTag, priority, weight)
		if err != nil {
			return err
		}
	}
	return nil
}

func UpdateChannelUsedQuota(id int, quota int) {
	if common.BatchUpdateEnabled {
		addNewRecord(BatchUpdateTypeChannelUsedQuota, id, quota)
		return
	}
	updateChannelUsedQuota(id, quota)
}

func updateChannelUsedQuota(id int, quota int) {
	err := DB.Model(&Channel{}).Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota)).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update channel used quota: channel_id=%d, delta_quota=%d, error=%v", id, quota, err))
	}
}

func DeleteChannelByStatus(status int64) (int64, error) {
	return deleteChannelsByStatuses([]int{int(status)})
}

func DeleteDisabledChannel() (int64, error) {
	return deleteChannelsByStatuses([]int{common.ChannelStatusAutoDisabled, common.ChannelStatusManuallyDisabled})
}

func deleteChannelsByStatuses(statuses []int) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	deletedIDs := make([]int, 0)
	err := DB.Transaction(func(tx *gorm.DB) error {
		var ids []int
		if err := lockForUpdate(tx).Model(&Channel{}).Where("status IN ?", statuses).Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			result := tx.Where("id = ? AND status IN ?", id, statuses).Delete(&Channel{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if err := tx.Where("channel_id = ?", id).Delete(&Ability{}).Error; err != nil {
				return err
			}
			if err := deleteChannelFailureStatesTx(tx, id); err != nil {
				return err
			}
			if tx.Migrator().HasTable(&ChannelSelectionMetricState{}) {
				if err := tx.Where("channel_id = ?", id).Delete(&ChannelSelectionMetricState{}).Error; err != nil {
					return err
				}
			}
			deletedIDs = append(deletedIDs, id)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	clearChannelSelectionMetricsForChannels(deletedIDs)
	return int64(len(deletedIDs)), nil
}

func GetPaginatedTags(offset int, limit int) ([]*string, error) {
	return GetPaginatedChannelTags(DB.Model(&Channel{}), offset, limit)
}

func GetPaginatedChannelTags(query *gorm.DB, offset int, limit int) ([]*string, error) {
	var tags []*string
	err := query.
		Select("DISTINCT tag").
		Where("tag is not null AND tag != ''").
		Order(clause.OrderByColumn{Column: clause.Column{Name: "tag"}}).
		Offset(offset).
		Limit(limit).
		Find(&tags).Error
	return tags, err
}

func SearchTags(keyword string, group string, model string, idSort bool) ([]*string, error) {
	var tags []*string
	modelsCol := "`models`"

	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		modelsCol = `"models"`
	}

	baseURLCol := "`base_url`"
	// 如果是 PostgreSQL，使用双引号
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		baseURLCol = `"base_url"`
	}

	order := "priority desc"
	if idSort {
		order = "id desc"
	}

	// 构造基础查询
	baseQuery := DB.Model(&Channel{}).Omit("key")

	// 构造WHERE子句
	whereClause := "(id = ? OR name LIKE ? OR " + commonKeyCol + " = ? OR " + baseURLCol + " LIKE ?) AND " + modelsCol + " LIKE ?"
	args := []any{common.String2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%", "%" + model + "%"}
	baseQuery = ApplyChannelGroupFilter(baseQuery.Where(whereClause, args...), group)

	subQuery := baseQuery.
		Select("tag").
		Where("tag != ''").
		Order(order)

	err := DB.Table("(?) as sub", subQuery).
		Select("DISTINCT tag").
		Find(&tags).Error

	if err != nil {
		return nil, err
	}

	return tags, nil
}

func (channel *Channel) ValidateSettings() error {
	channelParams := &dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		err := common.Unmarshal([]byte(*channel.Setting), channelParams)
		if err != nil {
			return err
		}
	}
	if _, err := common.ParseProxyURLStrict(channelParams.Proxy); err != nil {
		return fmt.Errorf("invalid channel proxy: %w", err)
	}
	if err := channelParams.ValidateHTTPTransport(); err != nil {
		return err
	}
	channelOtherSettings := &dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		err := common.UnmarshalJsonStr(channel.OtherSettings, channelOtherSettings)
		if err != nil {
			return err
		}
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if channelOtherSettings.AdvancedCustom == nil {
			return fmt.Errorf("advanced_custom is required")
		}
	}
	if channelOtherSettings.AdvancedCustom != nil {
		if err := channelOtherSettings.AdvancedCustom.Validate(); err != nil {
			return err
		}
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom && channelOtherSettings.UpstreamModelUpdateCheckEnabled {
		if _, ok := channelOtherSettings.AdvancedCustom.ModelListRoute(); !ok {
			return fmt.Errorf("advanced custom channels require a %s route when upstream model update checks are enabled", dto.AdvancedCustomModelListPath)
		}
	}
	return nil
}

func (channel *Channel) GetSetting() dto.ChannelSettings {
	setting := dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		err := common.Unmarshal([]byte(*channel.Setting), &setting)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal setting: channel_id=%d, error=%v", channel.Id, err))
			channel.Setting = nil // 清空设置以避免后续错误
			_ = channel.Save()    // 保存修改
		}
	}
	return setting
}

func (channel *Channel) SetSetting(setting dto.ChannelSettings) {
	settingBytes, err := common.Marshal(setting)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal setting: channel_id=%d, error=%v", channel.Id, err))
		return
	}
	channel.Setting = common.GetPointer[string](string(settingBytes))
}

func (channel *Channel) GetOtherSettings() dto.ChannelOtherSettings {
	setting := dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		err := common.UnmarshalJsonStr(channel.OtherSettings, &setting)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal setting: channel_id=%d, error=%v", channel.Id, err))
			channel.OtherSettings = "{}" // 清空设置以避免后续错误
			_ = channel.Save()           // 保存修改
		}
	}
	return setting
}

func (channel *Channel) SetOtherSettings(setting dto.ChannelOtherSettings) {
	settingBytes, err := common.Marshal(setting)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to marshal setting: channel_id=%d, error=%v", channel.Id, err))
		return
	}
	channel.OtherSettings = string(settingBytes)
}

func (channel *Channel) GetParamOverride() map[string]interface{} {
	paramOverride := make(map[string]interface{})
	if channel.ParamOverride != nil && *channel.ParamOverride != "" {
		err := common.Unmarshal([]byte(*channel.ParamOverride), &paramOverride)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal param override: channel_id=%d, error=%v", channel.Id, err))
		}
	}
	return paramOverride
}

func (channel *Channel) GetHeaderOverride() map[string]interface{} {
	headerOverride := make(map[string]interface{})
	if channel.HeaderOverride != nil && *channel.HeaderOverride != "" {
		err := common.Unmarshal([]byte(*channel.HeaderOverride), &headerOverride)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to unmarshal header override: channel_id=%d, error=%v", channel.Id, err))
		}
	}
	return headerOverride
}

func GetChannelsByIds(ids []int) ([]*Channel, error) {
	var channels []*Channel
	err := DB.Where("id in (?)", ids).Find(&channels).Error
	return channels, err
}

func BatchSetChannelTag(ids []int, tag *string) error {
	// 开启事务
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	// 更新标签
	err := tx.Model(&Channel{}).Where("id in (?)", ids).Update("tag", tag).Error
	if err != nil {
		tx.Rollback()
		return err
	}

	// update ability status
	channels, err := GetChannelsByIds(ids)
	if err != nil {
		tx.Rollback()
		return err
	}

	for _, channel := range channels {
		err = channel.UpdateAbilities(tx)
		if err != nil {
			tx.Rollback()
			return err
		}
	}

	// 提交事务
	return tx.Commit().Error
}

// CountAllChannels returns total channels in DB
func CountAllChannels() (int64, error) {
	var total int64
	err := DB.Model(&Channel{}).Count(&total).Error
	return total, err
}

// CountAllTags returns number of non-empty distinct tags
func CountAllTags() (int64, error) {
	return CountChannelTags(DB.Model(&Channel{}))
}

func CountChannelTags(query *gorm.DB) (int64, error) {
	var total int64
	err := query.Where("tag is not null AND tag != ''").Distinct("tag").Count(&total).Error
	return total, err
}

// Get channels of specified type with pagination
func GetChannelsByType(startIdx int, num int, idSort bool, channelType int) ([]*Channel, error) {
	var channels []*Channel
	order := "priority desc"
	if idSort {
		order = "id desc"
	}
	err := DB.Where("type = ?", channelType).Order(order).Limit(num).Offset(startIdx).Omit("key").Find(&channels).Error
	return channels, err
}

// Count channels of specific type
func CountChannelsByType(channelType int) (int64, error) {
	var count int64
	err := DB.Model(&Channel{}).Where("type = ?", channelType).Count(&count).Error
	return count, err
}

// Return map[type]count for all channels
func CountChannelsGroupByType() (map[int64]int64, error) {
	type result struct {
		Type  int64 `gorm:"column:type"`
		Count int64 `gorm:"column:count"`
	}
	var results []result
	err := DB.Model(&Channel{}).Select("type, count(*) as count").Group("type").Find(&results).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]int64)
	for _, r := range results {
		counts[r.Type] = r.Count
	}
	return counts, nil
}
