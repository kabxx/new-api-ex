package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAutoRetrySelectionTest(t *testing.T, groups []string, channels []model.Channel, abilities []model.Ability) *gin.Context {
	t.Helper()
	previousDB := model.DB
	previousCache := common.MemoryCacheEnabled
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	common.MemoryCacheEnabled = true
	groupsJSON, err := common.Marshal(groups)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(string(groupsJSON)))
	usable := make(map[string]string, len(groups))
	for _, group := range groups {
		usable[group] = group
	}
	usableJSON, err := common.Marshal(usable)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(string(usableJSON)))
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&abilities).Error)
	model.InitChannelCache()
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCache
		_ = setting.UpdateAutoGroupsByJsonString(previousAutoGroups)
		_ = setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups)
		if model.DB != nil {
			model.InitChannelCache()
		}
	})
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c
}

func TestAutoLegacyWithoutCrossGroupFallsBackOnceThenStaysSelected(t *testing.T) {
	priority := int64(100)
	weight := uint(100)
	c := setupAutoRetrySelectionTest(t,
		[]string{"empty", "fallback", "later"},
		[]model.Channel{
			{Id: 1, Name: "fallback", Key: "key-1", Group: "fallback", Models: "model", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight},
			{Id: 2, Name: "later", Key: "key-2", Group: "later", Models: "model", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight},
		},
		[]model.Ability{
			{Group: "fallback", Model: "model", ChannelId: 1, Enabled: true, Priority: &priority, Weight: weight},
			{Group: "later", Model: "model", ChannelId: 2, Enabled: true, Priority: &priority, Weight: weight},
		},
	)

	p := NewRetryParam(c, "auto", "model", "/v1/responses")
	first, group, err := CacheGetRandomSatisfiedChannel(p)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "fallback", group)
	common.SetContextKey(c, constant.ContextKeyChannelPriority, first.GetPriority())
	p.RecordSelection(first.Id, 0, false)
	p.IncreaseRetry()

	second, group, err := CacheGetRandomSatisfiedChannel(p)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 1, second.Id)
	assert.Equal(t, "fallback", group)
}

func TestAutoLegacyCrossGroupAdvancesPriorityAndGroupWithoutResettingAttempt(t *testing.T) {
	high, low := int64(100), int64(10)
	weight := uint(100)
	c := setupAutoRetrySelectionTest(t,
		[]string{"group-a", "group-b"},
		[]model.Channel{
			{Id: 1, Name: "a-high", Key: "key-1", Group: "group-a", Models: "model", Status: common.ChannelStatusEnabled, Priority: &high, Weight: &weight},
			{Id: 2, Name: "a-low", Key: "key-2", Group: "group-a", Models: "model", Status: common.ChannelStatusEnabled, Priority: &low, Weight: &weight},
			{Id: 3, Name: "b-high", Key: "key-3", Group: "group-b", Models: "model", Status: common.ChannelStatusEnabled, Priority: &high, Weight: &weight},
		},
		[]model.Ability{
			{Group: "group-a", Model: "model", ChannelId: 1, Enabled: true, Priority: &high, Weight: weight},
			{Group: "group-a", Model: "model", ChannelId: 2, Enabled: true, Priority: &low, Weight: weight},
			{Group: "group-b", Model: "model", ChannelId: 3, Enabled: true, Priority: &high, Weight: weight},
		},
	)
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, true)
	p := NewRetryParam(c, "auto", "model", "/v1/responses")

	wantIDs := []int{1, 2, 3}
	wantGroups := []string{"group-a", "group-a", "group-b"}
	for index := range wantIDs {
		channel, group, err := CacheGetRandomSatisfiedChannel(p)
		require.NoError(t, err)
		require.NotNil(t, channel)
		assert.Equal(t, wantIDs[index], channel.Id)
		assert.Equal(t, wantGroups[index], group)
		assert.Equal(t, index, p.GetRetry())
		common.SetContextKey(c, constant.ContextKeyChannelPriority, channel.GetPriority())
		p.RecordSelection(channel.Id, 0, false)
		p.IncreaseRetry()
	}

	channel, _, err := CacheGetRandomSatisfiedChannel(p)
	require.NoError(t, err)
	assert.Nil(t, channel)
	assert.True(t, p.CandidateExhausted)
	assert.Equal(t, 3, p.GetRetry())
}
