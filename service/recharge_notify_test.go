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
		Quota: 8_000_000, Status: common.UserStatusEnabled, AffCode: "alice-aff",
	}
	require.NoError(t, database.Create(&user).Error)
	for _, topUp := range []model.TopUp{
		{UserId: user.Id, Amount: 10, Money: 1.6, TradeNo: "paid-small", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusSuccess},
		{UserId: user.Id, Amount: 300, Money: 36, TradeNo: "paid-before", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusSuccess},
		{UserId: user.Id, Amount: 0, Money: 99, TradeNo: "subscription", Status: common.TopUpStatusSuccess},
		{UserId: user.Id, Amount: 5, Money: 36.5, TradeNo: "pending", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusPending},
		{UserId: user.Id, Amount: 5_000_000, Money: 1, TradeNo: "admin-credit", PaymentProvider: model.PaymentProviderAdmin, Status: common.TopUpStatusSuccess},
	} {
		require.NoError(t, database.Create(&topUp).Error)
	}
	smallPaidNotice, err := loadRechargeNotice("paid-small")
	require.NoError(t, err)
	assert.InDelta(t, 1.6, smallPaidNotice.PaidRMB, 0.001)
	assert.Equal(t, 5_000_000, smallPaidNotice.AddedQuota)
	paidNotice, err := loadRechargeNotice("paid-before")
	require.NoError(t, err)
	assert.InDelta(t, 36, paidNotice.PaidRMB, 0.001)
	assert.Equal(t, 150_000_000, paidNotice.AddedQuota)

	notice, err := loadRechargeNotice("admin-credit")
	require.NoError(t, err)
	assert.Equal(t, user.Id, notice.CustomerID)
	assert.Equal(t, "Alice Chen", notice.CustomerName)
	assert.Equal(t, "alice@example.com", notice.Email)
	assert.Equal(t, 5_000_000, notice.AddedQuota)
	assert.Equal(t, 8_000_000, notice.RemainingQuota)
	assert.InDelta(t, 1, notice.PaidRMB, 0.001)
	assert.InDelta(t, 38.6, notice.CumulativePaidRMB, 0.001)

	title, content := renderRechargeNotice("zh-CN", notice)
	assert.Contains(t, title, "Alice Chen")
	assert.Contains(t, content, "ID：")
	assert.Contains(t, content, "alice@example.com")
	assert.Contains(t, content, "¥1.00")
	assert.Contains(t, content, "¥38.60")
}
