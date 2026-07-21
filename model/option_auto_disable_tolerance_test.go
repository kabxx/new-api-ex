package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutoDisableToleranceValidationPrecedesPersistence(t *testing.T) {
	previousDB := DB
	previousTolerance := common.AutoDisableTolerance
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{"AutoDisableTolerance": "2"}
	common.OptionMapRWMutex.Unlock()
	common.AutoDisableTolerance = 2

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Option{}))

	t.Cleanup(func() {
		DB = previousDB
		common.AutoDisableTolerance = previousTolerance
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			_ = sqlDB.Close()
		}
	})

	for _, value := range []string{"-1", "0.5", "1000", "invalid"} {
		err := UpdateOption("AutoDisableTolerance", value)
		assert.ErrorContains(t, err, "must be an integer between 0 and 999")
	}

	err = UpdateOptionsBulk(map[string]string{
		"AutoDisableTolerance": "-1",
		"UnrelatedOption":      "value",
	})
	assert.ErrorContains(t, err, "must be an integer between 0 and 999")

	var optionCount int64
	require.NoError(t, db.Model(&Option{}).Count(&optionCount).Error)
	assert.Zero(t, optionCount)
	assert.Equal(t, 2, common.AutoDisableTolerance)
	assert.Equal(t, "2", common.OptionMap["AutoDisableTolerance"])

	err = updateOptionMap("AutoDisableTolerance", "0.5")
	assert.ErrorContains(t, err, "must be an integer between 0 and 999")
	assert.Equal(t, 2, common.AutoDisableTolerance)
	assert.Equal(t, "2", common.OptionMap["AutoDisableTolerance"])

	require.NoError(t, UpdateOption("AutoDisableTolerance", "999"))
	var option Option
	require.NoError(t, db.First(&option, "key = ?", "AutoDisableTolerance").Error)
	assert.Equal(t, "999", option.Value)
	assert.Equal(t, 999, common.AutoDisableTolerance)
	assert.Equal(t, "999", common.OptionMap["AutoDisableTolerance"])
}
