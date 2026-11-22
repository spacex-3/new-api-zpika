package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLoadAndRenderRechargeNotice(t *testing.T) {
	require.NoError(t, i18n.Init())
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.TopUp{}))
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })

	user := model.User{
		Username: "alice", DisplayName: "Alice Chen", Email: "alice@example.com",
		Quota: 3_500_000, Status: common.UserStatusEnabled, AffCode: "alice-aff",
	}
	require.NoError(t, database.Create(&user).Error)
	for _, topUp := range []model.TopUp{
		{UserId: user.Id, Amount: 10, Money: 73, TradeNo: "paid-before", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusSuccess},
		{UserId: user.Id, Amount: 0, Money: 99, TradeNo: "subscription", Status: common.TopUpStatusSuccess},
		{UserId: user.Id, Amount: 5, Money: 36.5, TradeNo: "pending", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusPending},
		{UserId: user.Id, Amount: 500_000, Money: 7.3, TradeNo: "admin-credit", PaymentProvider: model.PaymentProviderAdmin, Status: common.TopUpStatusSuccess},
	} {
		require.NoError(t, database.Create(&topUp).Error)
	}

	notice, err := loadRechargeNotice("admin-credit")
	require.NoError(t, err)
	assert.Equal(t, user.Id, notice.CustomerID)
	assert.Equal(t, "Alice Chen", notice.CustomerName)
	assert.Equal(t, "alice@example.com", notice.Email)
	assert.Equal(t, 500_000, notice.AddedQuota)
	assert.Equal(t, 3_500_000, notice.RemainingQuota)
	assert.InDelta(t, 7.3, notice.PaidRMB, 0.001)
	assert.InDelta(t, 80.3, notice.CumulativePaidRMB, 0.001)

	title, content := renderRechargeNotice("zh-CN", notice)
	assert.Contains(t, title, "Alice Chen")
	assert.Contains(t, content, "ID：")
	assert.Contains(t, content, "alice@example.com")
	assert.Contains(t, content, "¥7.30")
	assert.Contains(t, content, "¥80.30")
}
